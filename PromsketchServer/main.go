package main

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "net/http/pprof"

	"github.com/SieDeta/promsketch_std/promsketch"
	"github.com/gin-gonic/gin"
	"github.com/zzylol/prometheus-sketches/model/labels"
	"github.com/zzylol/prometheus-sketches/promql/parser"
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

var ps *promsketch.PromSketches
var cloudEndpoint = os.Getenv("FORWARD_ENDPOINT")

func init() {
	ps = promsketch.NewPromSketches()
	log.Println("PromSketches instance initialized")
}

func main() {
	go logIngestionRate()

	router := gin.Default()
	router.POST("/ingest", handleIngest)
	router.GET("/query", handleQuery)
	router.GET("/throughput_test", runThroughputTest)
	router.GET("/parse", handleParse)
	router.GET("/debug-state", handleDebugState)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "UP", "message": "PromSketch Go server is running."})
	})

	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	log.Printf("PromSketch Go server listening on :7000")
	if err := router.Run(":7000"); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}

}

// handleDebugState akan memeriksa 10000 time series pertama
// dan mencetak yang mana saja yang sudah memiliki data sketch.
func handleDebugState(c *gin.Context) {
	log.Println("[DEBUG] Starting state check...")
	foundCount := 0
	detail := []string{}

	// Kita periksa dari machine_0 hingga machine_9999
	for i := 0; i < 10000; i++ {
		machineID := fmt.Sprintf("machine_%d", i)
		lsetBuilder := labels.NewBuilder(labels.Labels{})
		lsetBuilder.Set("machineid", machineID)
		lsetBuilder.Set(labels.MetricName, "fake_metric") // Ganti sesuai metric kamu
		lset := lsetBuilder.Labels()

		minTime, maxTime := ps.PrintCoverage(lset, "avg_over_time") // Ganti ke fungsi yang kamu uji

		if minTime != -1 {
			foundCount++
			log.Printf("[DEBUG] Data found for %s | Coverage: %d -> %d", machineID, minTime, maxTime)
			detail = append(detail, fmt.Sprintf("machine_%d: %d → %d", i, minTime, maxTime))
		}
	}

	if foundCount == 0 {
		log.Println("[DEBUG] State check finished. NO active sketches found.")
		c.JSON(http.StatusOK, gin.H{
			"status":  "state check finished",
			"message": "No active sketches found.",
		})
	} else {
		log.Printf("[DEBUG] State check finished. Found %d active sketches.", foundCount)
		c.JSON(http.StatusOK, gin.H{
			"status":          "state check finished",
			"found_sketches":  foundCount,
			"sketch_coverage": detail,
		})
	}
}

var totalIngested int64

// func handleIngest(c *gin.Context) {
// 	var payload IngestPayload
// 	if err := c.ShouldBindJSON(&payload); err != nil {
// 		log.Printf("Error binding JSON payload: %v", err)
// 		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid JSON payload: %v", err.Error())})
// 		return
// 	}

// 	start := time.Now()
// 	var wg sync.WaitGroup

// 	for _, metric := range payload.Metrics {
// 		wg.Add(1)
// 		go func(metric MetricPayload) {
// 			defer wg.Done()

// 			log.Printf("[INGEST] name=%s labels=%v value=%.2f", metric.Name, metric.Labels, metric.Value)

// 			lsetBuilder := labels.NewBuilder(labels.Labels{})
// 			// Tambahkan label dari payload (cth: machineid="machine_0")
// 			for k, v := range metric.Labels {
// 				lsetBuilder.Set(k, v)
// 			}
// 			lsetBuilder.Set(labels.MetricName, metric.Name) // labels.MetricName = "__name__"
// 			lset := lsetBuilder.Labels()

// 			log.Printf("[INGEST] Inserting to sketch: name=%s labels=%v ts=%d value=%.2f", metric.Name, metric.Labels, payload.Timestamp, metric.Value)

// 			if err := ps.SketchInsert(lset, payload.Timestamp, metric.Value); err != nil {
// 				log.Printf("[INGEST ERROR] Failed to insert sketch: %v", err)
// 			}
// 			atomic.AddInt64(&totalIngested, 1)

// 		}(metric)
// 	}

// 	wg.Wait()
// 	totalDuration := time.Since(start).Milliseconds()
// 	log.Printf("[BATCH COMPLETED] Processed %d metrics in %dms", len(payload.Metrics), totalDuration)

// 	go forwardToCloud(payload)

// 	c.JSON(http.StatusOK, gin.H{"status": "success", "ingested_metrics_count": len(payload.Metrics)})
// }

func handleIngest(c *gin.Context) {
	var payload IngestPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		log.Printf("Error binding JSON payload: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Invalid JSON payload: %v", err.Error())})
		return
	}

	start := time.Now()
	var wg sync.WaitGroup

	for _, metric := range payload.Metrics {
		wg.Add(1)
		go func(metric MetricPayload) {
			defer wg.Done()

			log.Printf("[INGEST] name=%s labels=%v value=%.2f", metric.Name, metric.Labels, metric.Value)

			lsetBuilder := labels.NewBuilder(labels.Labels{})
			// Tambahkan label dari payload (cth: machineid="machine_0")
			for k, v := range metric.Labels {
				lsetBuilder.Set(k, v)
			}
			lsetBuilder.Set(labels.MetricName, metric.Name) // labels.MetricName = "__name__"
			lset := lsetBuilder.Labels()

			log.Printf("[INGEST] Inserting to sketch: name=%s labels=%v ts=%d value=%.2f", metric.Name, metric.Labels, payload.Timestamp, metric.Value)

			if err := ps.SketchInsert(lset, payload.Timestamp, metric.Value); err == nil {
				atomic.AddInt64(&totalIngested, 1)

				// Push raw metric to Prometheus
				labels := map[string]string{}
				for k, v := range metric.Labels {
					labels[k] = v
				}
				labels["__name__"] = metric.Name
				pushSyntheticResult(metric.Name, labels, metric.Value, payload.Timestamp)
			}

		}(metric)
	}

	wg.Wait()
	totalDuration := time.Since(start).Milliseconds()
	log.Printf("[BATCH COMPLETED] Processed %d metrics in %dms", len(payload.Metrics), totalDuration)

	go forwardToCloud(payload)

	c.JSON(http.StatusOK, gin.H{"status": "success", "ingested_metrics_count": len(payload.Metrics)})
}

func pushSyntheticResult(metricName string, labels map[string]string, value float64, timestamp int64) {
	pushgatewayURL := os.Getenv("PUSHGATEWAY_URL")
	if pushgatewayURL == "" {
		pushgatewayURL = "http://localhost:9091"
	}

	labelParts := []string{}
	for k, v := range labels {
		if k == "__name__" {
			continue // jangan push label __name__
		}
		labelParts = append(labelParts, fmt.Sprintf("%s=\"%s\"", k, v))
	}

	labelStr := strings.Join(labelParts, ",")

	body := fmt.Sprintf("%s{%s} %f\n", metricName, labelStr, value)

	instanceID := labels["machineid"]
	if instanceID == "" {
		instanceID = "default"
	}

	url := fmt.Sprintf("%s/metrics/job/promsketch_push/instance/%s", pushgatewayURL, instanceID)

	log.Printf("[PUSH DEBUG] Pushing to %s with body:\n%s", url, body)

	req, err := http.NewRequest("PUT", url, strings.NewReader(body))
	if err != nil {
		log.Printf("[PUSH ERROR] request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "text/plain")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[PUSH ERROR] send: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		log.Printf("[PUSH ERROR] status: %s", resp.Status)
	} else {
		log.Printf("[PUSH OK] metric=%s labels=%v value=%.2f", metricName, labels, value)
	}
}

// func forwardToCloud(payload IngestPayload) {
// 	if cloudEndpoint == "" {
// 		log.Printf("[FORWARD] Skipped: FORWARD_ENDPOINT not set")
// 		return
// 	}
// 	data, err := json.Marshal(payload)
// 	if err != nil {
// 		log.Printf("[FORWARD ERROR] marshal: %v", err)
// 		return
// 	}
// 	req, err := http.NewRequest("POST", cloudEndpoint, bytes.NewBuffer(data))
// 	if err != nil {
// 		log.Printf("[FORWARD ERROR] request: %v", err)
// 		return
// 	}
// 	req.Header.Set("Content-Type", "application/json")

// 	client := &http.Client{Timeout: 5 * time.Second}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		log.Printf("[FORWARD ERROR] send: %v", err)
// 		return
// 	}
// 	defer resp.Body.Close()

// 	if resp.StatusCode >= 300 {
// 		log.Printf("[FORWARD ERROR] status: %s", resp.Status)
// 	} else {
// 		log.Printf("[FORWARD] success: %d bytes sent", len(data))
// 	}
// }

func forwardToCloud(payload IngestPayload) {
	for _, metric := range payload.Metrics {
		url := fmt.Sprintf("http://pushgateway:9091/metrics/job/promsketch/instance/%s", metric.Labels["machineid"])
		body := fmt.Sprintf("fake_metric{machineid=\"%s\"} %f\n", metric.Labels["machineid"], metric.Value)
		resp, err := http.Post(url, "text/plain", strings.NewReader(body))
		if err != nil {
			log.Printf("[PUSHGATEWAY ERROR] %v", err)
			continue
		}
		resp.Body.Close()
	}
}

func logIngestionRate() {
	var lastTotal int64 = 0

	// file log CSV
	file, err := os.OpenFile("throughput_log.csv", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("Failed to open throughput log file: %v", err)
	}
	defer file.Close()

	fileInfo, _ := file.Stat()
	if fileInfo.Size() == 0 {
		file.WriteString("timestamp,samples_per_sec,total_samples\n")
	}

	for {
		time.Sleep(5 * time.Second)

		current := atomic.LoadInt64(&totalIngested)
		rate := float64(current-lastTotal) / 5.0
		timestamp := time.Now().Format(time.RFC3339)

		log.Printf("[SERVER SPEED] Received %.2f samples/sec (Total: %d)", rate, current)

		// Save to CSV file
		entry := fmt.Sprintf("%s,%.2f,%d\n", timestamp, rate, current)
		if _, err := file.WriteString(entry); err != nil {
			log.Printf("[CSV LOG ERROR] %v", err)
		}

		lastTotal = current
	}
}

func handleQuery(c *gin.Context) {
	funcName := c.Query("func")
	metricName := c.Query("metric")
	mintStr := c.Query("mint")
	maxtStr := c.Query("maxt")

	mint, err := strconv.ParseInt(mintStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'mint' parameter."})
		return
	}
	maxt, err := strconv.ParseInt(maxtStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid 'maxt' parameter."})
		return
	}

	otherArgs := 0.0
	if argStr := c.Query("args"); argStr != "" {
		otherArgs, _ = strconv.ParseFloat(argStr, 64)
	}

	lsetBuilder := labels.NewBuilder(labels.Labels{})
	for k, v := range c.Request.URL.Query() {
		if strings.HasPrefix(k, "label_") {
			labelKey := k[len("label_"):len(k)]
			lsetBuilder.Set(labelKey, v[0])
		}
	}
	lsetBuilder.Set("fake_metric", metricName)
	lset := lsetBuilder.Labels()

	curTime := time.Now().UnixMilli()
	if !ps.LookUp(lset, funcName, mint, maxt) {
		c.JSON(http.StatusAccepted, gin.H{"status": "pending"})
		return
	}
	vector, annotations := ps.Eval(funcName, lset, otherArgs, mint, maxt, curTime)
	results := []map[string]interface{}{}
	for _, s := range vector {
		if !math.IsNaN(s.F) && s.T != 0 {
			results = append(results, map[string]interface{}{"value": s.F, "timestamp": s.T})
		}
	}
	resp := gin.H{"status": "success", "data": results}
	if len(annotations) > 0 {
		resp["annotations"] = annotations
	}
	c.JSON(http.StatusOK, resp)
}

// func handleParse(c *gin.Context) {
// 	query := c.Query("q")
// 	if query == "" {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing query parameter 'q'"})
// 		return
// 	}

// 	expr, err := parser.ParseExpr(query)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Parse error: %v", err)})
// 		return
// 	}

// 	c.JSON(http.StatusOK, gin.H{
// 		"status": "success",
// 		"ast":    expr.String(),
// 	})
// }

func handleParse(c *gin.Context) {
	query := c.Query("q")
	if query == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing query parameter 'q'"})
		return
	}
	log.Printf("[QUERY ENGINE] Received query: %s", query)

	expr, err := parser.ParseExpr(query)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Parse error: %v", err)})
		return
	}

	call, ok := expr.(*parser.Call)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Query must be a function call (e.g., avg_over_time(...))"})
		return
	}

	funcName := call.Func.Name
	otherArgs := 0.0

	matrixSelectorArg := call.Args[len(call.Args)-1]
	rangeArg, ok := matrixSelectorArg.(*parser.MatrixSelector)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The last argument must be a range vector (e.g., metric[60000ms])"})
		return
	}

	if len(call.Args) > 1 {
		firstArg, ok := call.Args[0].(*parser.NumberLiteral)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Numeric argument (like quantile) must be a number"})
			return
		}
		otherArgs = firstArg.Val
	}

	vs, ok := rangeArg.VectorSelector.(*parser.VectorSelector)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse vector selector inside the range vector"})
		return
	}
	metricName := vs.Name
	labelMap := make(map[string]string)
	for _, matcher := range vs.LabelMatchers {
		if matcher.Type == labels.MatchEqual {
			labelMap[matcher.Name] = matcher.Value
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only '=' label matchers are supported"})
			return
		}
	}

	lsetBuilder := labels.NewBuilder(labels.Labels{})
	for k, v := range labelMap {
		lsetBuilder.Set(k, v)
	}
	lsetBuilder.Set(labels.MetricName, metricName)
	lset := lsetBuilder.Labels()

	log.Printf("[QUERY ENGINE] Parsed components: func=%s, lset=%v, otherArgs=%.2f", funcName, lset, otherArgs)

	// Calculate the time range based on the range
	queryDuration := rangeArg.Range
	maxt := time.Now().UnixMilli()
	mint := maxt - queryDuration.Milliseconds()

	// GEt sketch value
	sketchMin, sketchMax := ps.PrintCoverage(lset, funcName)
	log.Printf("[SKETCH COVERAGE] Sketch coverage: min=%d max=%d | requested mint=%d maxt=%d", sketchMin, sketchMax, mint, maxt)

	// If there's no data in sketch
	if sketchMin == -1 || sketchMax == -1 {
		log.Printf("[QUERY ENGINE] No sketch coverage available yet for %v. Creating instance.", lset)
		ps.NewSketchCacheInstance(lset, funcName, queryDuration.Milliseconds(), 100000, 10000.0)

		c.JSON(http.StatusAccepted, gin.H{
			"status":  "pending",
			"message": "Sketch data is being prepared. Please try again in a few moments.",
		})
		return
	}

	// COrrection if requested query outside the range
	if mint < sketchMin {
		mint = sketchMin
	}
	if maxt > sketchMax {
		maxt = sketchMax
	}

	// Validasi akhir
	if maxt <= mint {
		c.JSON(http.StatusBadRequest, gin.H{
			"status":  "failed",
			"message": "Query time range is outside of sketch data coverage.",
		})
		return
	}

	curTime := maxt

	log.Printf("[QUERY ENGINE] Final adjusted range: mint=%d maxt=%d", mint, maxt)

	vector, annotations := ps.Eval(funcName, lset, otherArgs, mint, maxt, curTime)

	results := []map[string]interface{}{}
	for _, sample := range vector {
		if !math.IsNaN(sample.F) && sample.T != 0 {
			results = append(results, map[string]interface{}{
				"timestamp": sample.T,
				"value":     sample.F,
			})

			// Push synthetic query result
			syntheticLabels := map[string]string{
				"machineid":       lset.Get("machineid"),
				"original_metric": lset.Get("__name__"),
				"function":        funcName,
			}
			if funcName == "quantile_over_time" {
				syntheticLabels["quantile"] = fmt.Sprintf("%.2f", otherArgs)
			}
			pushSyntheticResult("promsketch_query_result", syntheticLabels, sample.F, sample.T)
		}
		pushSyntheticResult(
			"promsketch_query_result",
			map[string]string{
				"machineid": labelMap["machineid"],
				"function":  funcName,
			},
			sample.F,
			sample.T,
		)
	}

	log.Printf("[QUERY ENGINE] Evaluation successful. Returning %d data points.", len(results))
	response := gin.H{
		"status": "success",
		"data":   results,
	}
	if len(annotations) > 0 {
		response["annotations"] = annotations
	}

	c.JSON(http.StatusOK, response)
}

// func handleParse(c *gin.Context) {
// 	query := c.Query("q")
// 	if query == "" {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing query parameter 'q'"})
// 		return
// 	}
// 	log.Printf("[QUERY ENGINE] Received query: %s", query)

// 	expr, err := parser.ParseExpr(query)
// 	if err != nil {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("Parse error: %v", err)})
// 		return
// 	}

// 	call, ok := expr.(*parser.Call)
// 	if !ok {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Query must be a function call (e.g., avg_over_time(...))"})
// 		return
// 	}

// 	funcName := call.Func.Name
// 	otherArgs := 0.0

// 	matrixSelectorArg := call.Args[len(call.Args)-1]
// 	rangeArg, ok := matrixSelectorArg.(*parser.MatrixSelector)
// 	if !ok {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "The last argument must be a range vector (e.g., metric[60000ms])"})
// 		return
// 	}

// 	if len(call.Args) > 1 {
// 		firstArg, ok := call.Args[0].(*parser.NumberLiteral)
// 		if !ok {
// 			c.JSON(http.StatusBadRequest, gin.H{"error": "Numeric argument (like quantile) must be a number"})
// 			return
// 		}
// 		otherArgs = firstArg.Val
// 	}

// 	vs, ok := rangeArg.VectorSelector.(*parser.VectorSelector)
// 	if !ok {
// 		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to parse vector selector inside the range vector"})
// 		return
// 	}
// 	metricName := vs.Name
// 	labelMap := make(map[string]string)
// 	for _, matcher := range vs.LabelMatchers {
// 		if matcher.Type == labels.MatchEqual {
// 			labelMap[matcher.Name] = matcher.Value
// 		} else {
// 			c.JSON(http.StatusBadRequest, gin.H{"error": "Only '=' label matchers are supported"})
// 			return
// 		}
// 	}

// 	lsetBuilder := labels.NewBuilder(labels.Labels{})
// 	for k, v := range labelMap {
// 		lsetBuilder.Set(k, v)
// 	}
// 	lsetBuilder.Set(labels.MetricName, metricName)
// 	lset := lsetBuilder.Labels()

// 	log.Printf("[QUERY ENGINE] Parsed components: func=%s, lset=%v, otherArgs=%.2f", funcName, lset, otherArgs)

// 	// Calculate the time range based on the range
// 	queryDuration := rangeArg.Range
// 	maxt := time.Now().UnixMilli()
// 	mint := maxt - queryDuration.Milliseconds()

// 	// GEt sketch value
// 	sketchMin, sketchMax := ps.PrintCoverage(lset, funcName)
// 	log.Printf("[SKETCH COVERAGE] Sketch coverage: min=%d max=%d | requested mint=%d maxt=%d", sketchMin, sketchMax, mint, maxt)

// 	// If there's no data in sketch
// 	if sketchMin == -1 || sketchMax == -1 {
// 		log.Printf("[QUERY ENGINE] No sketch coverage available yet for %v. Creating instance.", lset)
// 		ps.NewSketchCacheInstance(lset, funcName, queryDuration.Milliseconds(), 100000, 10000.0)

// 		c.JSON(http.StatusAccepted, gin.H{
// 			"status":  "pending",
// 			"message": "Sketch data is being prepared. Please try again in a few moments.",
// 		})
// 		return
// 	}

// 	// COrrection if requested query outside the range
// 	if mint < sketchMin {
// 		mint = sketchMin
// 	}
// 	if maxt > sketchMax {
// 		maxt = sketchMax
// 	}

// 	// Validasi akhir
// 	if maxt <= mint {
// 		c.JSON(http.StatusBadRequest, gin.H{
// 			"status":  "failed",
// 			"message": "Query time range is outside of sketch data coverage.",
// 		})
// 		return
// 	}

// 	curTime := maxt

// 	log.Printf("[QUERY ENGINE] Final adjusted range: mint=%d maxt=%d", mint, maxt)

// 	vector, annotations := ps.Eval(funcName, lset, otherArgs, mint, maxt, curTime)

// 	results := []map[string]interface{}{}
// 	for _, sample := range vector {
// 		if !math.IsNaN(sample.F) {
// 			results = append(results, map[string]interface{}{
// 				"timestamp": sample.T,
// 				"value":     sample.F,
// 			})
// 		}
// 	}

// 	log.Printf("[QUERY ENGINE] Evaluation successful. Returning %d data points.", len(results))
// 	response := gin.H{
// 		"status": "success",
// 		"data":   results,
// 	}
// 	if len(annotations) > 0 {
// 		response["annotations"] = annotations
// 	}

// 	c.JSON(http.StatusOK, response)
// }

func runThroughputTest(c *gin.Context) {
	num := 10000
	start := time.Now()

	var wg sync.WaitGroup
	var success int64

	for i := 0; i < num; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			machineID := fmt.Sprintf("bench_%d", i)
			lset := labels.FromStrings("machineid", machineID, "fake_metric", "benchmark_metric")
			timestamp := time.Now().UnixMilli()
			value := float64(i % 1000)

			if err := ps.SketchInsertInsertionThroughputTest(lset, timestamp, value); err == nil {
				atomic.AddInt64(&success, 1)
			} else {
				log.Printf("[Error] SketchInsertInsertionThroughputTest failed: %v", err)
			}
		}(i)
	}

	wg.Wait()
	elapsed := time.Since(start).Seconds()
	rate := float64(success) / elapsed

	log.Printf("[THROUGHPUT TEST] Inserted %d samples in %.2fs (%.2f samples/sec)", success, elapsed, rate)

	c.JSON(http.StatusOK, gin.H{
		"inserted_samples": success,
		"duration_seconds": elapsed,
		"throughput":       fmt.Sprintf("%.2f samples/sec", rate),
	})
}

// func runThroughputTestTimed(c *gin.Context) {
// 	var (
// 		success int64
// 		stop    int32
// 		wg      sync.WaitGroup
// 	)

// 	// Timer stop: set stop = 1 after 5 seconds
// 	go func() {
// 		time.Sleep(5 * time.Second)
// 		atomic.StoreInt32(&stop, 1)
// 	}()

// 	start := time.Now()
// 	for i := 0; ; i++ {
// 		if atomic.LoadInt32(&stop) == 1 {
// 			break
// 		}
// 		wg.Add(1)
// 		go func(i int) {
// 			defer wg.Done()
// 			machineID := fmt.Sprintf("bench_timed_%d", i)
// 			lset := labels.FromStrings("machineid", machineID, "fake_metric", "benchmark_metric")
// 			timestamp := time.Now().UnixMilli()
// 			value := float64(i % 1000)

// 			if err := ps.SketchInsertInsertionThroughputTest(lset, timestamp, value); err == nil {
// 				atomic.AddInt64(&success, 1)
// 			}
// 		}(i)
// 	}

// 	wg.Wait()
// 	elapsed := time.Since(start).Seconds()
// 	rate := float64(success) / elapsed

// 	log.Printf("[TIMED THROUGHPUT TEST] Inserted %d samples in %.2fs (%.2f samples/sec)", success, elapsed, rate)

// 	c.JSON(http.StatusOK, gin.H{
// 		"inserted_samples": success,
// 		"duration_seconds": elapsed,
// 		"throughput":       fmt.Sprintf("%.2f samples/sec", rate),
// 	})
// }
