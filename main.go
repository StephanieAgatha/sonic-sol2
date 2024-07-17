package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blocto/solana-go-sdk/client"
	"github.com/blocto/solana-go-sdk/common"
	"github.com/blocto/solana-go-sdk/program/system"
	"github.com/blocto/solana-go-sdk/rpc"
	"github.com/blocto/solana-go-sdk/types"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	delayRetry   = 2 * time.Second
	minSolAmount = 0.001
	maxSolAmount = 0.01
	challengeURL = "https://odyssey-api-beta.sonic.game/auth/sonic/challenge"
	authorizeURL = "https://odyssey-api-beta.sonic.game/auth/sonic/authorize"
)

var headers = map[string]string{
	"Accept":             "*/*",
	"Accept-Encoding":    "gzip, deflate, br, zstd",
	"Accept-Language":    "en-US,en;q=0.9",
	"Cache-Control":      "no-cache",
	"Origin":             "https://odyssey.sonic.game",
	"Pragma":             "no-cache",
	"Priority":           "u=1, i",
	"Referer":            "https://odyssey.sonic.game/",
	"Sec-Ch-Ua":          `"Not/A)Brand";v="8", "Chromium";v="126", "Microsoft Edge";v="126"`,
	"Sec-Ch-Ua-Mobile":   "?0",
	"Sec-Ch-Ua-Platform": `"macOS"`,
	"Sec-Fetch-Dest":     "empty",
	"Sec-Fetch-Mode":     "cors",
	"Sec-Fetch-Site":     "same-site",
	"Sec-Gpc":            "1",
	"User-Agent":         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/97.0.4692.71 Safari/537.36",
}

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

func getToken(privateKey string) (string, error) {
	account, err := types.AccountFromBase58(privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to get account from private key: %w", err)
	}

	challengeReq, err := http.NewRequest("GET", challengeURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create challenge request: %w", err)
	}
	q := challengeReq.URL.Query()
	q.Add("wallet", account.PublicKey.ToBase58())
	challengeReq.URL.RawQuery = q.Encode()

	for key, value := range headers {
		challengeReq.Header.Set(key, value)
	}

	client := &http.Client{}
	challengeResp, err := client.Do(challengeReq)
	if err != nil {
		return "", fmt.Errorf("failed to get challenge: %w", err)
	}
	defer challengeResp.Body.Close()

	var challengeData struct {
		Data string `json:"data"`
	}
	if err := json.NewDecoder(challengeResp.Body).Decode(&challengeData); err != nil {
		return "", fmt.Errorf("failed to decode challenge response: %w", err)
	}

	signature := ed25519.Sign(account.PrivateKey, []byte(challengeData.Data))
	signatureBase64 := base64.StdEncoding.EncodeToString(signature)

	publicKeyBytes := account.PublicKey.Bytes()
	publicKeyBase64 := base64.StdEncoding.EncodeToString(publicKeyBytes)

	authPayload := map[string]string{
		"address":         account.PublicKey.ToBase58(),
		"address_encoded": publicKeyBase64,
		"signature":       signatureBase64,
	}

	authPayloadJSON, err := json.Marshal(authPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal auth payload: %w", err)
	}

	authReq, err := http.NewRequest("POST", authorizeURL, bytes.NewBuffer(authPayloadJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create auth request: %w", err)
	}

	for key, value := range headers {
		authReq.Header.Set(key, value)
	}
	authReq.Header.Set("Content-Type", "application/json")

	authResp, err := client.Do(authReq)
	if err != nil {
		return "", fmt.Errorf("failed to send auth request: %w", err)
	}
	defer authResp.Body.Close()

	var authData struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(authResp.Body).Decode(&authData); err != nil {
		return "", fmt.Errorf("failed to decode auth response: %w", err)
	}

	return authData.Data.Token, nil
}

func getTxMilestone(authToken string) {
	url := "https://odyssey-api-beta.sonic.game/user/transactions/state/daily"
	method := "GET"

	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Add("Authorization", authToken)
	req.Header.Add("User-Agent", "Mozilla/5.0 Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/14.0.3 Safari/605.1.15")

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

func claimReward(authToken string, stage int) {
	url := "https://odyssey-api-beta.sonic.game/user/transactions/rewards/claim"
	method := "POST"

	payload := map[string]int{"stage": stage}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		fmt.Println("Failed to marshal payload:", err)
		return
	}

	client := &http.Client{}
	req, err := http.NewRequest(method, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		fmt.Println("Failed to create request:", err)
		return
	}
	req.Header.Add("Authorization", authToken)
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/97.0.4692.71 Safari/537.36")

	res, err := client.Do(req)
	if err != nil {
		fmt.Println("Failed to send request:", err)
		return
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println("Failed to read response body:", err)
		return
	}

	fmt.Println("Raw response:", string(body))

	if res.StatusCode != http.StatusOK {
		fmt.Printf("Unexpected status code: %d\n", res.StatusCode)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Println("Failed to unmarshal response:", err)
		return
	}

	if code, ok := result["code"].(float64); ok && code == 100015 {
		fmt.Printf("Already claimed stage %d\n", stage)
		return
	}

	if status, ok := result["status"].(string); ok && status == "success" {
		fmt.Printf("Claimed reward stage %d\n", stage)
	} else {
		fmt.Printf("Failed to claim reward stage %d\n", stage)
	}
}

func main() {
	initLogger()
	rand.Seed(time.Now().UnixNano())

	privateKeys, err := readPrivateKeys("pk.txt")
	if err != nil {
		log.Error().Err(err).Msg("Failed to read private key file")
		return
	}

	fmt.Print("Do you want to use Authorization key for claiming rewards? (y/n): ")
	reader := bufio.NewReader(os.Stdin)
	useAuthInput, _ := reader.ReadString('\n')
	useAuthInput = strings.TrimSpace(strings.ToLower(useAuthInput))

	useAuth := useAuthInput == "y"

	rpcSonic := "https://devnet.sonic.game"
	rpcClient := client.NewClient(rpcSonic)

	fmt.Print("How many addresses do you want to generate: ")
	addressCountInput, _ := reader.ReadString('\n')
	addressCountInput = strings.TrimSpace(addressCountInput)
	addressCount, err := strconv.Atoi(addressCountInput)
	if err != nil {
		log.Error().Err(err).Msg("Invalid number of addresses")
		return
	}

	fmt.Print("Input delay (in seconds): ")
	delayInput, _ := reader.ReadString('\n')
	delayInput = strings.TrimSpace(delayInput)
	delay, err := strconv.Atoi(delayInput)
	if err != nil {
		log.Error().Err(err).Msg("Invalid delay input")
		return
	}

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

		log.Info().
			Str("wallet", accountFrom.PublicKey.ToBase58()).
			Float64("balance", float64(balance)/1_000_000_000).
			Msg("Wallet balance")

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

					randomAmount := minSolAmount + rand.Float64()*(maxSolAmount-minSolAmount)
					solAmount := uint64(randomAmount * 1_000_000_000)

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
				time.Sleep(time.Duration(delay) * time.Second)
			}(address)
		}

		wg.Wait()

		if useAuth {
			authToken, err := getToken(privateKeyBase58)
			if err != nil {
				log.Error().Err(err).Msg("Failed to get authorization token")
				continue
			}

			fmt.Println("==================")
			fmt.Printf("Fetching transactions for wallet: %s\n", accountFrom.PublicKey.ToBase58())
			getTxMilestone(authToken)
			fmt.Println("==================")

			fmt.Println("Sleeping 10 second for claiming...")
			time.Sleep(10 * time.Second)

			for stage := 1; stage <= 3; stage++ {
				fmt.Printf("Claiming reward stage %d for wallet: %s\n", stage, accountFrom.PublicKey.ToBase58())
				claimReward(authToken, stage)
				time.Sleep(3 * time.Second)
				fmt.Println("Done")
			}
		}
	}

	endTime := time.Now()
	duration := endTime.Sub(startTime)
	log.Info().Msgf("Successfully sent to %d addresses, and it took %.2f seconds\n", addressCount, duration.Seconds())
}
