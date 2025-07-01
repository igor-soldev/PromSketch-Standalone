// package main

// import (
// 	"bytes"
// 	"encoding/json"
// 	"fmt"
// 	"net/http"
// 	"sync"
// 	"time"
// )

// type IngestPayload struct {
// 	Timestamp int64           `json:"timestamp"`
// 	Metrics   []MetricPayload `json:"metrics"`
// }

// type MetricPayload struct {
// 	Name   string            `json:"name"`
// 	Labels map[string]string `json:"labels"`
// 	Value  float64           `json:"value"`
// }

// func main() {
// 	const (
// 		threads    = 1000
// 		iterations = 1000
// 		totalReqs  = threads * iterations
// 		url        = "http://localhost:7000/ingest"
// 	)

// 	var wg sync.WaitGroup
// 	start := time.Now()

// 	for i := 0; i < threads; i++ {
// 		wg.Add(1)
// 		go func(threadID int) {
// 			defer wg.Done()
// 			for j := 0; j < iterations; j++ {
// 				payload := IngestPayload{
// 					Timestamp: time.Now().UnixMilli(),
// 					Metrics: []MetricPayload{
// 						{
// 							Name: "benchmark_metric",
// 							Labels: map[string]string{
// 								"machineid": fmt.Sprintf("loadgen_%d_%d", threadID, j),
// 							},
// 							Value: float64(j),
// 						},
// 					},
// 				}
// 				data, _ := json.Marshal(payload)
// 				resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
// 				if err != nil {
// 					fmt.Println("Request failed:", err)
// 					continue
// 				}
// 				resp.Body.Close()
// 			}
// 		}(i)
// 	}

// 	wg.Wait()
// 	duration := time.Since(start).Seconds()
// 	fmt.Printf("Sent %d requests in %.2f seconds (%.2f req/sec)\n", totalReqs, duration, float64(totalReqs)/duration)
// }

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

const (
	serverURL       = "http://localhost:7000/ingest"
	numClients      = 100                  // number of goroutine paralel
	scrapeInterval  = 1 * time.Millisecond // interval scraping tinggi
	metricsPerBatch = 100000               // number metrics per request
)

type IngestPayload struct {
	Timestamp int64           `json:"timestamp"`
	Metrics   []MetricPayload `json:"metrics"`
}

type MetricPayload struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

func generateMetrics(id int) []MetricPayload {
	metrics := make([]MetricPayload, 0, metricsPerBatch)
	for i := 0; i < metricsPerBatch; i++ {
		m := MetricPayload{
			Name: fmt.Sprintf("cpu_usage"),
			Labels: map[string]string{
				"job":       "stress_exporter",
				"instance":  fmt.Sprintf("client_%d_metric_%d", id, i),
				"machineid": fmt.Sprintf("machine_%d", i),
			},
			Value: rand.Float64() * 100,
		}
		metrics = append(metrics, m)
	}
	return metrics
}

func startClient(id int, wg *sync.WaitGroup) {
	defer wg.Done()
	client := &http.Client{}

	for {
		payload := IngestPayload{
			Timestamp: time.Now().UnixMilli(),
			Metrics:   generateMetrics(id),
		}
		data, _ := json.Marshal(payload)

		req, err := http.NewRequest("POST", serverURL, bytes.NewBuffer(data))
		if err != nil {
			log.Printf("[Client %d] Error creating request: %v", id, err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[Client %d] Error sending request: %v", id, err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("[Client %d] Bad response: %s", id, resp.Status)
		}

		time.Sleep(scrapeInterval)
	}
}

func main() {
	log.Printf("Starting stress test with %d clients, scrape interval = %v, %d metrics/batch", numClients, scrapeInterval, metricsPerBatch)

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go startClient(i, &wg)
	}
	wg.Wait()
}
