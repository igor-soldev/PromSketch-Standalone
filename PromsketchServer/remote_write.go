package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/prometheus/prompb"
	"github.com/zzylol/prometheus-sketch-VLDB/prometheus-sketches/model/labels"
)

type remoteWriter struct {
	endpoint string       // remote write endpoint URL
	client   *http.Client // HTTP client with bounded timeout
	queue    chan IngestPayload
	lastTS   map[string]int64 // last timestamp per series (enforce monotonic writes)
}

// newRemoteWriter builds a remoteWriter and starts its background worker.
func newRemoteWriter(endpoint string, timeout time.Duration) *remoteWriter {
	rw := &remoteWriter{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: timeout,
		},
		queue:  make(chan IngestPayload, 64),
		lastTS: make(map[string]int64),
	}
	go rw.run()
	return rw
}

// Forward enqueues a payload for asynchronous remote write.
func (rw *remoteWriter) Forward(payload IngestPayload) {
	if rw == nil || len(payload.Metrics) == 0 {
		return
	}
	select {
	case rw.queue <- payload:
	default:
		log.Printf("[REMOTE WRITE] Dropping payload: buffer full")
	}
}

// run drains queued payloads and flushes them sequentially.
func (rw *remoteWriter) run() {
	for payload := range rw.queue {
		rw.flush(payload)
	}
}

// flush serializes a payload into a WriteRequest and posts it.
func (rw *remoteWriter) flush(payload IngestPayload) {
	timeseries, _ := buildRemoteWriteSeries(payload, rw.lastTS)
	if len(timeseries) == 0 {
		return
	}
	req := &prompb.WriteRequest{Timeseries: timeseries}
	data, err := proto.Marshal(req)
	if err != nil {
		log.Printf("[REMOTE WRITE] Failed to marshal request: %v", err)
		return
	}
	compressed := snappy.Encode(nil, data)
	httpReq, err := http.NewRequest(http.MethodPost, rw.endpoint, bytes.NewReader(compressed))
	if err != nil {
		log.Printf("[REMOTE WRITE] Failed to build request: %v", err)
		return
	}
	httpReq.Header.Set("Content-Encoding", "snappy")
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	httpReq.Header.Set("User-Agent", "promsketch-remote-writer/1.0")

	resp, err := rw.client.Do(httpReq)
	if err != nil {
		log.Printf("[REMOTE WRITE] Delivery error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("[REMOTE WRITE] Failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

// buildRemoteWriteSeries converts metrics into prompb TimeSeries while enforcing monotonic timestamps.
func buildRemoteWriteSeries(payload IngestPayload, last map[string]int64) ([]prompb.TimeSeries, int) {
	seriesMap := make(map[string]*prompb.TimeSeries)
	adjusted := 0
	for _, metric := range payload.Metrics {
		sampleTs := payload.Timestamp
		lbls, key := buildRemoteLabels(metric)
		if prev, ok := last[key]; ok && sampleTs <= prev {
			sampleTs = prev + 1
			adjusted++
		}
		ts := seriesMap[key]
		if ts == nil {
			ts = &prompb.TimeSeries{Labels: lbls}
			seriesMap[key] = ts
		}
		ts.Samples = append(ts.Samples, prompb.Sample{
			Value:     metric.Value,
			Timestamp: sampleTs,
		})
		last[key] = sampleTs
	}
	result := make([]prompb.TimeSeries, 0, len(seriesMap))
	for _, ts := range seriesMap {
		result = append(result, *ts)
	}
	return result, adjusted
}

// buildRemoteLabels returns sorted remote-write labels plus a unique cache key.
func buildRemoteLabels(metric MetricPayload) ([]prompb.Label, string) {
	labelsList := make([]prompb.Label, 0, len(metric.Labels)+1)
	labelsList = append(labelsList, prompb.Label{
		Name:  labels.MetricName,
		Value: metric.Name,
	})
	for k, v := range metric.Labels {
		labelsList = append(labelsList, prompb.Label{Name: k, Value: v})
	}
	sort.Slice(labelsList, func(i, j int) bool {
		return labelsList[i].Name < labelsList[j].Name
	})
	var b strings.Builder
	for _, lbl := range labelsList {
		b.WriteString(lbl.Name)
		b.WriteByte('=')
		b.WriteString(lbl.Value)
		b.WriteByte(',')
	}
	return labelsList, b.String()
}
