package main

import (
	"flag"
	"fmt"
	"net/http"
	"sync"
	"time"
)

func main() {
	concurrency := flag.Int("c", 100, "Number of concurrent connections")
	requests := flag.Int("n", 10000, "Number of requests to make")
	url := flag.String("url", "http://localhost:8080/", "URL to benchmark")
	flag.Parse()

	fmt.Printf("Benchmarking %s with %d requests using %d concurrent connections\n",
		*url, *requests, *concurrency)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: *concurrency,
			DisableKeepAlives:   false,
		},
	}

	var wg sync.WaitGroup
	requestsPerWorker := *requests / *concurrency
	if *requests%*concurrency != 0 {
		requestsPerWorker++
	}

	startTime := time.Now()
	successCount := 0
	errorCount := 0
	var mu sync.Mutex

	var responseTimes []time.Duration
	var responseTimeMu sync.Mutex

	for i := range *concurrency {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for j := range requestsPerWorker {
				reqNum := workerID*requestsPerWorker + j
				if reqNum >= *requests {
					break
				}

				startReq := time.Now()
				resp, err := client.Get(*url)
				reqDuration := time.Since(startReq)

				responseTimeMu.Lock()
				responseTimes = append(responseTimes, reqDuration)
				responseTimeMu.Unlock()

				mu.Lock()
				if err != nil {
					errorCount++
					if errorCount <= 10 { // limit error output
						fmt.Printf("Request error: %v\n", err)
					}
				} else {
					successCount++
					resp.Body.Close()
				}
				mu.Unlock()

				// Periodically report progress
				if reqNum%1000 == 0 {
					mu.Lock()
					fmt.Printf("Completed %d requests (success: %d, errors: %d)\n",
						reqNum, successCount, errorCount)
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	var totalTime time.Duration
	var minTime time.Duration = time.Hour
	var maxTime time.Duration

	for _, t := range responseTimes {
		totalTime += t
		if t < minTime {
			minTime = t
		}
		if t > maxTime {
			maxTime = t
		}
	}

	avgTime := totalTime / time.Duration(len(responseTimes))

	fmt.Printf("\nBenchmark Results:\n")
	fmt.Printf("Total requests: %d\n", *requests)
	fmt.Printf("Successful requests: %d\n", successCount)
	fmt.Printf("Failed requests: %d\n", errorCount)
	fmt.Printf("Total time: %v\n", duration)
	fmt.Printf("Requests per second: %.2f\n", float64(*requests)/duration.Seconds())
	fmt.Printf("Min response time: %v\n", minTime)
	fmt.Printf("Avg response time: %v\n", avgTime)
	fmt.Printf("Max response time: %v\n", maxTime)
}
