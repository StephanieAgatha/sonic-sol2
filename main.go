package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/blocto/solana-go-sdk/rpc"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blocto/solana-go-sdk/client"
	"github.com/blocto/solana-go-sdk/common"
	"github.com/blocto/solana-go-sdk/program/system"
	"github.com/blocto/solana-go-sdk/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	delayRetry = 2 * time.Second
)

func initLogger() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
}

func readPrivateKeys(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var privateKeys []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		privateKey := strings.TrimSpace(scanner.Text())
		if privateKey != "" {
			privateKeys = append(privateKeys, privateKey)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if len(privateKeys) == 0 {
		return nil, fmt.Errorf("No wallets found in pk.txt file")
	}

	return privateKeys, nil
}

func main() {
	initLogger()
	privateKeys, err := readPrivateKeys("pk.txt")
	if err != nil {
		log.Error().Err(err).Msg("Failed to read private key file")
		return
	}

	const minSolAmount = 0.001

	rpcSonic := "https://devnet.sonic.game"
	rpcClient := client.NewClient(rpcSonic)

	fmt.Print("How many amount sol do you want to transfer? (minimum is 0.001 SOL) : ")
	reader := bufio.NewReader(os.Stdin)
	amountInput, _ := reader.ReadString('\n')
	amountInput = strings.TrimSpace(amountInput)
	amount, err := strconv.ParseFloat(amountInput, 64)
	if err != nil {
		log.Error().Err(err).Msg("Invalid transfer amount")
		return
	}

	if amount < minSolAmount {
		fmt.Printf("Transfer amount (%.9f SOL) is below minimum (%.9f SOL). Setting transfer amount to minimum.\n", amount, minSolAmount)
		amount = minSolAmount
	}

	solAmount := uint64(amount * 1_000_000_000) // convert to lamports (1 SOL = 1,000,000,000 lamports)

	fmt.Print("How many addresses do you want to generate: ")
	addressCountInput, _ := reader.ReadString('\n')
	addressCountInput = strings.TrimSpace(addressCountInput)
	addressCount, err := strconv.Atoi(addressCountInput)
	if err != nil {
		log.Error().Err(err).Msg("Invalid number of addresses")
		return
	}

	// Get delay
	fmt.Print("Input delay (in seconds): ")
	delayInput, _ := reader.ReadString('\n')
	delayInput = strings.TrimSpace(delayInput)
	delay, err := strconv.Atoi(delayInput)
	if err != nil {
		log.Error().Err(err).Msg("Invalid delay input")
		return
	}

	// Get Authorization value from user
	fmt.Print("Enter Authorization key (or press enter to skip): ")
	authKey, _ := reader.ReadString('\n')
	authKey = strings.TrimSpace(authKey)

	var wg sync.WaitGroup
	startTime := time.Now()

	for _, privateKeyBase58 := range privateKeys {
		accountFrom, err := types.AccountFromBase58(privateKeyBase58)
		if err != nil {
			log.Error().Err(err).Msg("Failed to generate keypair")
			continue
		}

		if privateKeyBase58 == "" {
			log.Error().Msg("No private keys found")
			continue
		}

		// Check balance
		balanceResult, err := rpcClient.GetBalance(context.TODO(), accountFrom.PublicKey.ToBase58())
		if err != nil {
			log.Error().Err(err).Msg("Failed to get balance")
			continue
		}

		balance := balanceResult
		if balance == 0 {
			log.Error().Msg("No balance available")
			continue
		}

		// Print balance
		log.Info().
			Str("wallet", accountFrom.PublicKey.ToBase58()).
			Float64("balance", float64(balance)/1_000_000_000).
			Msg("Wallet balance")

		requiredBalance := solAmount * uint64(addressCount)
		if balance < requiredBalance {
			log.Error().
				Uint64("balance now", balance).
				Uint64("required", requiredBalance).
				Msg("Insufficient balance")
			continue
		}

		var addresses []common.PublicKey
		for i := 0; i < addressCount; i++ {
			newKeypair := types.NewAccount()
			addresses = append(addresses, newKeypair.PublicKey)
			fmt.Printf("Generated address %d: %s\n", i+1, newKeypair.PublicKey.ToBase58())
		}

		for _, address := range addresses {
			wg.Add(1)
			go func(address common.PublicKey) {
				defer wg.Done()
				for {
					var blockhashResponse rpc.GetLatestBlockhashValue
					var err error
					for {
						blockhashResponse, err = rpcClient.GetLatestBlockhash(context.TODO())
						if err == nil {
							break
						}
						log.Error().Msg("Failed to get blockhash, retrying...")
						time.Sleep(delayRetry)
					}

					instruction := system.Transfer(system.TransferParam{
						From:   accountFrom.PublicKey,
						To:     address,
						Amount: solAmount,
					})

					message := types.NewMessage(types.NewMessageParam{
						FeePayer:        accountFrom.PublicKey,
						RecentBlockhash: blockhashResponse.Blockhash,
						Instructions:    []types.Instruction{instruction},
					})

					tx, err := types.NewTransaction(types.NewTransactionParam{
						Message: message,
						Signers: []types.Account{accountFrom},
					})
					if err != nil {
						log.Error().Msg("Failed to create transaction")
						continue
					}

					// Send transaction with retries
					for {
						txHash, err := rpcClient.SendTransaction(context.TODO(), tx)
						if err == nil {
							log.Info().
								Str("to address", address.ToBase58()).
								Str("tx hash", txHash).
								Float64("amount", float64(solAmount)/1_000_000_000).
								Msg("Successfully sent SOL")
							break
						}
						log.Error().
							Str("to address", address.ToBase58()).
							Msg("Failed to send transaction, retrying...")
						time.Sleep(delayRetry)
					}
					break
				}

				// If user input a jwt token then fetch tx total
				if authKey != "" {
					fmt.Println("==================")
					fmt.Println("Fetch transaction from sonic server ...")
					getTxMilestone(authKey)
					fmt.Println("==================")
				}

				// Delay
				time.Sleep(time.Duration(delay) * time.Second)
			}(address)
		}
	}

	wg.Wait()

	endTime := time.Now()
	duration := endTime.Sub(startTime)
	log.Info().Msgf("Successfully sent to %d addresses, and it took %.2f seconds\n", addressCount, duration.Seconds())
}

func getTxMilestone(authKey string) {
	url := "https://odyssey-api.sonic.game/user/transactions/state/daily"
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Add("Authorization", authKey)

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Println("Failed to unmarshal response:", err)
		return
	}

	if data, ok := result["data"].(map[string]interface{}); ok {
		if totalTransactions, ok := data["total_transactions"].(float64); ok {
			fmt.Printf("Total transactions: %.0f\n", totalTransactions)
		} else {
			fmt.Println("total_transactions not found ", err)
		}
	} else {
		fmt.Println("failed to fetch data", err)
	}
}
