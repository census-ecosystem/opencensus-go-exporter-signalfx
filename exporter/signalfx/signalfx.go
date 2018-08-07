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
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/signalfx/golib/datapoint"
	"github.com/signalfx/golib/sfxclient"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
)

// Exporter exports stats to SignalFx
type Exporter struct {
	// Options used to register and log stats
	opts   Options
	c      *collector
	client *sfxclient.Scheduler
}

// Options contains options for configuring the exporter.
type Options struct {
	// Token contains the required token to send datapoints
	// to SignalFx. It cannot be empty
	Token string

	// DatapointEndpoint contains the endpoint to send the datapoints
	// to SignalFx. The default value can be found as sfxclient.IngestEndpointV2
	DatapointEndpoint string

	// ReportingDelay contains the reporting interval.
	// The default value is 20s
	ReportingDelay time.Duration

	// OnError is the hook to be called when there is
	// an error uploading the stats or tracing data.
	// If no custom hook is set, errors are logged.
	// Optional.
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
		opts:   o,
		c:      collector,
		client: sfxclient.NewScheduler(),
	}

	if e.opts.DatapointEndpoint != "" {
		e.client.Sink.(*sfxclient.HTTPSink).DatapointEndpoint = e.opts.DatapointEndpoint
	}
	e.client.Sink.(*sfxclient.HTTPSink).AuthToken = e.opts.Token

	if e.opts.ReportingDelay != 0 {
		e.client.ReportingDelay(e.opts.ReportingDelay)
	}

	go e.client.Schedule(context.Background())

	return e, nil
}

var _ view.Exporter = (*Exporter)(nil)

// registerViews creates the view map and prevents duplicated views
func (c *collector) registerViews(views ...*view.View) {
	for _, thisView := range views {
		sig := viewSignature(thisView.Name, thisView)
		c.registeredViewsMu.Lock()
		_, ok := c.registeredViews[sig]
		c.registeredViewsMu.Unlock()
		if !ok {
			desc := sanitize(thisView.Name)
			c.registeredViewsMu.Lock()
			c.registeredViews[sig] = desc
			c.registeredViewsMu.Unlock()
		}
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

// toMetric receives the view data information and creates metrics that are adequate according to
// graphite documentation.
func (c *collector) toMetric(v *view.View, row *view.Row, vd *view.Data, e *Exporter) signalFxMetric {
	switch data := row.Data.(type) {
	case *view.CountData:
		metric := signalFxMetric{
			metricName:     sanitize(vd.View.Name),
			metricType:     "cumulative_counter",
			metricValueInt: data.Value,
			timestamp:      time.Now(),
			dimensions:     buildDimensions(row.Tags),
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
			metricValue: data.Value,
			timestamp:   time.Now(),
			dimensions:  buildDimensions(row.Tags),
		}
		return metric
	default:
		e.opts.onError(fmt.Errorf("aggregation %T is not yet supported", data))
		return signalFxMetric{}
	}
}

// extractViewData extracts stats data
func extractViewData(vd *view.Data, e *Exporter) {
	for _, row := range vd.Rows {
		metric := e.c.toMetric(vd.View, row, vd, e)
		go sendRequest(e, metric)
	}
}

// buildDimensions uses the tag values and keys to create
// the dimensions used by SignalFx
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

// addViewData assigns the view data to the correct view
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

// newCollector returns a collector struct
func newCollector(opts Options) *collector {
	return &collector{
		opts:            opts,
		registeredViews: make(map[string]string),
		viewData:        make(map[string]*view.Data),
	}
}

// viewName builds a unique name composed of the namespace
// and the sanitized view name
func viewName(namespace string, v *view.View) string {
	var name string
	if namespace != "" {
		name = namespace + "_"
	}
	return name + sanitize(v.Name)
}

// viewSignature builds a signature that will identify a view
// The signature consists of the namespace, the viewName and the
// list of tags. Example: Namespace_viewName-tagName...
func viewSignature(namespace string, v *view.View) string {
	var buf bytes.Buffer
	buf.WriteString(viewName(namespace, v))
	for _, k := range v.TagKeys {
		buf.WriteString("-" + k.Name())
	}
	return buf.String()
}

// sendRequest sends a package of data containing a metric
func sendRequest(e *Exporter, data signalFxMetric) {
	ctx := context.Background()
	switch data.metricType {
	case "gauge":
		err := e.client.Sink.AddDatapoints(ctx, []*datapoint.Datapoint{
			sfxclient.GaugeF(data.metricName, data.dimensions, data.metricValue),
		})
		if err != nil {
			e.opts.onError(fmt.Errorf("error sending datapoint to SignalFx: %T", err))
		}
	case "cumulative_counter":
		err := e.client.Sink.AddDatapoints(ctx, []*datapoint.Datapoint{
			sfxclient.Cumulative(data.metricName, data.dimensions, data.metricValueInt),
		})
		if err != nil {
			e.opts.onError(fmt.Errorf("error sending datapoint to SignalFx: %T", err))
		}
	default:
		e.opts.onError(fmt.Errorf("metric type not supported: %s", data.metricType))
	}
}

const labelKeySizeLimit = 128

// Sanitize returns a string that is truncated to 128 characters if it's too
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

// sanitizeRune converts anything that is not a letter or digit to an underscore
func sanitizeRune(r rune) rune {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return r
	}
	// Everything else turns into an underscore
	return '_'
}
