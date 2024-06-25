package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blocto/solana-go-sdk/client"
	"github.com/blocto/solana-go-sdk/common"
	"github.com/blocto/solana-go-sdk/program/system"
	"github.com/blocto/solana-go-sdk/types"
)

const solAmount = uint64(300000) // 0.0003 in lamports

func main() {
	// Load private keys from pk.txt
	privateKeys, err := readPrivateKeys("pk.txt")
	if err != nil {
		log.Fatalf("Failed to read private keys: %v", err)
	}

	rpcSonic := "https://devnet.sonic.game"
	rpcClient := client.NewClient(rpcSonic)

	fmt.Print("How many addresses do you want to generate: ")
	reader := bufio.NewReader(os.Stdin)
	addressCountInput, _ := reader.ReadString('\n')
	addressCountInput = strings.TrimSpace(addressCountInput)
	addressCount, err := strconv.Atoi(addressCountInput)
	if err != nil {
		log.Fatalf("Invalid number of addresses: %v", err)
	}

	// Get delay
	fmt.Print("Input delay (dalam detik): ")
	delayInput, _ := reader.ReadString('\n')
	delayInput = strings.TrimSpace(delayInput)
	delay, err := strconv.Atoi(delayInput)
	if err != nil {
		log.Fatalf("Invalid delay input: %v", err)
	}

	// Get Authorization value from user
	fmt.Print("Enter Authorization key (or press enter to skip): ")
	authKey, _ := reader.ReadString('\n')
	authKey = strings.TrimSpace(authKey)

	for _, privateKeyBase58 := range privateKeys {
		accountFrom, err := types.AccountFromBase58(privateKeyBase58)
		if err != nil {
			log.Fatalf("Failed to generate keypair: %v", err)
		}

		// Check balance
		balanceResult, err := rpcClient.GetBalance(context.TODO(), accountFrom.PublicKey.ToBase58())
		if err != nil {
			log.Fatalf("Failed to get balance: %v", err)
		}

		balance := balanceResult
		if balance == 0 {
			log.Fatalf("No balance available")
		}

		// Print balance
		fmt.Printf("Balance for wallet %s: %.9f SOL\n", accountFrom.PublicKey.ToBase58(), float64(balance)/1_000_000_000)

		requiredBalance := solAmount * uint64(addressCount)
		if balance < requiredBalance {
			log.Fatalf("Insufficient balance. Required: %d, Available: %d", requiredBalance, balance)
		}

		var addresses []common.PublicKey
		for i := 0; i < addressCount; i++ {
			newKeypair := types.NewAccount()
			addresses = append(addresses, newKeypair.PublicKey)
			fmt.Printf("Generated address %d: %s\n", i+1, newKeypair.PublicKey.ToBase58())
		}

		for _, address := range addresses {
			recentBlockhashResponse, err := rpcClient.GetLatestBlockhash(context.TODO())
			if err != nil {
				log.Fatalf("Failed to get latest blockhash: %v", err)
			}

			instruction := system.Transfer(system.TransferParam{
				From:   accountFrom.PublicKey,
				To:     address,
				Amount: solAmount,
			})

			message := types.NewMessage(types.NewMessageParam{
				FeePayer:        accountFrom.PublicKey,
				RecentBlockhash: recentBlockhashResponse.Blockhash,
				Instructions:    []types.Instruction{instruction},
			})

			tx, err := types.NewTransaction(types.NewTransactionParam{
				Message: message,
				Signers: []types.Account{accountFrom},
			})
			if err != nil {
				log.Fatalf("Failed to create transaction: %v", err)
			}

			// Send transaction
			txHash, err := rpcClient.SendTransaction(context.TODO(), tx)
			if err != nil {
				log.Printf("Failed to send transaction to %s: %v", address.ToBase58(), err)
			} else {
				fmt.Printf("Success sending to %s with transaction hash %s\n", address.ToBase58(), txHash)
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
		}
	}
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
	return privateKeys, nil
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
			fmt.Println("total_transactions not found ", err.Error())
		}
	} else {
		fmt.Println("failed to fetch data", err.Error())
	}
}
