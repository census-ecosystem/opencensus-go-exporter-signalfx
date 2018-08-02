// Copyright 2018, OpenCensus Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package signalfx contains a SignalFx exporter that supports exporting
// OpenCensus views as SignalFx data points.
package signalfx // import "opencensus-go-signalfx/exporter/signalfx"

import (
	"bytes"
	"context"
	"log"
	"sync"
	"time"

	"errors"
	"fmt"
	"sort"

	"strings"
	"unicode"

	"github.com/signalfx/golib/datapoint"
	"github.com/signalfx/golib/sfxclient"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

// Exporter exports stats to SignalFx
type Exporter struct {
	// Options used to register and log stats
	opts Options
	c    *collector
}

// Options contains options for configuring the exporter.
type Options struct {
	// Token contains the required token to send datapoints
	// to SignalFx. It cannot be empty
	Token string

	// DatapointEndpoint contains the endpoint to send the datapoints
	// to SignalFx.
	DatapointEndpoint string

	OnError func(err error)
}

// NewExporter returns an exporter that exports stats to SignalFx.
func NewExporter(o Options) (*Exporter, error) {
	if o.Token == "" {
		err := errors.New(fmt.Sprintf("token cannot be empty on options %T", o))
		return nil, err
	}

	collector := newCollector(o)
	e := &Exporter{
		opts: o,
		c:    collector,
	}

	return e, nil
}

var _ view.Exporter = (*Exporter)(nil)

// registerViews creates the view map and prevents duplicated views
func (c *collector) registerViews(views ...*view.View) {
	count := 0
	for _, view := range views {
		sig := viewSignature(view.Name, view)
		c.registeredViewsMu.Lock()
		_, ok := c.registeredViews[sig]
		c.registeredViewsMu.Unlock()
		if !ok {
			desc := sanitize(view.Name)
			c.registeredViewsMu.Lock()
			c.registeredViews[sig] = desc
			c.registeredViewsMu.Unlock()
			count++
		}
	}
	if count == 0 {
		return
	}
}

func (o *Options) onError(err error) {
	if o.OnError != nil {
		o.OnError(err)
	} else {
		log.Printf("Failed to export to SignalFx: %v", err)
	}
}

// ExportView exports to the SignalFx if view data has one or more rows.
// Each OpenCensus stats records will be converted to
// corresponding SignalFx Metric
func (e *Exporter) ExportView(vd *view.Data) {
	if len(vd.Rows) == 0 {
		return
	}
	e.c.addViewData(vd)

	extractViewData(vd, e)
}

func (c *collector) formatMetric(v *view.View, row *view.Row, vd *view.Data, e *Exporter) signalFxMetric {
	switch data := row.Data.(type) {
	case *view.CountData:
		metric := signalFxMetric{
			metricName:     sanitize(vd.View.Name),
			metricType:     "cumulative_counter",
			metricValueInt: int64(data.Value),
			timestamp:      time.Now(),
			dimensions:     buildDimensions(row.Tags),
		}
		return metric
	case *view.DistributionData:
		indicesMap := make(map[float64]int)
		buckets := make([]float64, 0, len(v.Aggregation.Buckets))
		for i, b := range v.Aggregation.Buckets {
			if _, ok := indicesMap[b]; !ok {
				indicesMap[b] = i
				buckets = append(buckets, b)
			}
		}
		sort.Float64s(buckets)
		var values []int64
		for _, bucket := range buckets {
			values = append(values, int64(bucket))
		}

		metric := signalFxMetric{
			metricName: sanitize(vd.View.Name),
			metricType: "cumulative_bucket",
			timestamp:  time.Now(),
			dimensions: buildDimensions(row.Tags),
			buckets:    values,
		}
		return metric
	case *view.SumData:
		metric := signalFxMetric{
			metricName:     sanitize(vd.View.Name),
			metricType:     "cumulative_counter",
			metricValueInt: int64(data.Value),
			timestamp:      time.Now(),
			dimensions:     buildDimensions(row.Tags),
		}
		return metric
	case *view.LastValueData:
		metric := signalFxMetric{
			metricName:  sanitize(vd.View.Name),
			metricType:  "gauge",
			metricValue: float64(data.Value),
			timestamp:   time.Now(),
			dimensions:  buildDimensions(row.Tags),
		}
		return metric
	default:
		e.opts.OnError(errors.New(fmt.Sprintf("aggregation %T is not yet supported", data)))
		return signalFxMetric{}
	}
}

// extractViewData extracts stats data
func extractViewData(vd *view.Data, e *Exporter) {
	for _, row := range vd.Rows {
		metric := e.c.formatMetric(vd.View, row, vd, e)
		go sendRequest(e, metric)
	}
}

func buildDimensions(t []tag.Tag) map[string]string {
	values := make(map[string]string)
	for _, t := range t {
		values[t.Key.Name()] = t.Value
	}
	return values
}

type collector struct {
	opts Options
	mu   sync.Mutex // mu guards all the fields.

	// viewData are accumulated and atomically
	// appended to on every Export invocation, from
	// stats. These views are cleared out when
	// Collect is invoked and the cycle is repeated.
	viewData map[string]*view.Data

	registeredViewsMu sync.Mutex

	registeredViews map[string]string
}

func (c *collector) addViewData(vd *view.Data) {
	c.registerViews(vd.View)
	sig := viewSignature(vd.View.Name, vd.View)

	c.mu.Lock()
	c.viewData[sig] = vd
	c.mu.Unlock()
}

type signalFxMetric struct {
	metricType     string
	metricName     string
	metricValue    float64
	metricValueInt int64
	timestamp      time.Time
	dimensions     map[string]string
	buckets        []int64
}

func newCollector(opts Options) *collector {
	return &collector{
		opts:            opts,
		registeredViews: make(map[string]string),
		viewData:        make(map[string]*view.Data),
	}
}

func viewName(namespace string, v *view.View) string {
	var name string
	if namespace != "" {
		name = namespace + "_"
	}
	return name + sanitize(v.Name)
}

func viewSignature(namespace string, v *view.View) string {
	var buf bytes.Buffer
	buf.WriteString(viewName(namespace, v))
	for _, k := range v.TagKeys {
		buf.WriteString("-" + k.Name())
	}
	return buf.String()
}

// sendRequest sends a package of data containing one metric
func sendRequest(e *Exporter, data signalFxMetric) {
	client := sfxclient.NewHTTPSink()
	if e.opts.DatapointEndpoint != "" {
		client.DatapointEndpoint = e.opts.DatapointEndpoint
	}
	client.AuthToken = e.opts.Token
	ctx := context.Background()

	switch data.metricType {
	case "gauge":
		err := client.AddDatapoints(ctx, []*datapoint.Datapoint{
			sfxclient.GaugeF(data.metricName, data.dimensions, data.metricValue),
		})
		if err != nil {
			e.opts.OnError(errors.New(fmt.Sprintf("Error sending datapoint to SignalFx: %T", err)))
		}
	case "cumulative_counter":
		err := client.AddDatapoints(ctx, []*datapoint.Datapoint{
			sfxclient.Cumulative(data.metricName, data.dimensions, data.metricValueInt),
		})
		if err != nil {
			e.opts.OnError(errors.New(fmt.Sprintf("Error sending datapoint to SignalFx: %T", err)))
		}
	case "cumulative_bucket":
		bucket := sfxclient.CumulativeBucket{
			MetricName: data.metricName,
			Dimensions: data.dimensions,
		}

		for _, value := range data.buckets {
			bucket.Add(value)
		}

		err := client.AddDatapoints(ctx, bucket.Datapoints())
		if err != nil {
			e.opts.OnError(errors.New(fmt.Sprintf("Error sending datapoint to SignalFx: %T", err)))
		}
	default:
		e.opts.OnError(errors.New(fmt.Sprintf("Metric type not supported: %s", data.metricType)))
	}
}

func (c *collector) cloneViewData() map[string]*view.Data {
	c.mu.Lock()
	defer c.mu.Unlock()

	viewDataCopy := make(map[string]*view.Data)
	for sig, viewData := range c.viewData {
		viewDataCopy[sig] = viewData
	}
	return viewDataCopy
}

const labelKeySizeLimit = 128

// Sanitize returns a string that is trunacated to 100 characters if it's too
// long, and replaces non-alphanumeric characters to underscores.
func sanitize(s string) string {
	if len(s) == 0 {
		return s
	}
	if len(s) > labelKeySizeLimit {
		s = s[:labelKeySizeLimit]
	}
	s = strings.Map(sanitizeRune, s)
	if unicode.IsDigit(rune(s[0])) {
		s = "key_" + s
	}
	if s[0] == '_' {
		s = "key" + s
	}
	return s
}

// converts anything that is not a letter or digit to an underscore
func sanitizeRune(r rune) rune {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return r
	}
	// Everything else turns into an underscore
	return '_'
}
