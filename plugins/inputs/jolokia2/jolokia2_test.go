package jolokia2

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/influxdata/telegraf/testutil"
	"github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/require"
)

const validThreeLevelMultiValueJSON = `
[
  {
    "request":{
      "mbean":"java.lang:type=*",
      "type":"read"
    },
    "value":{
      "java.lang:type=Memory":{
        "ObjectPendingFinalizationCount":0,
        "Verbose":false,
        "HeapMemoryUsage":{
          "init":134217728,
          "committed":173015040,
          "max":1908932608,
          "used":16840016
        },
        "NonHeapMemoryUsage":{
          "init":2555904,
          "committed":51380224,
          "max":-1,
          "used":49944048
        },
        "ObjectName":{
          "objectName":"java.lang:type=Memory"
        }
      }
    },
    "timestamp":1446129191,
    "status":200
  }
]`

const validBulkResponseJSON = `
[
  {
    "request":{
      "mbean":"java.lang:type=Memory",
      "attribute":"HeapMemoryUsage",
      "type":"read"
    },
    "value":{
      "init":67108864,
      "committed":456130560,
      "max":477626368,
      "used":203288528
    },
    "timestamp":1446129191,
    "status":200
  },
  {
    "request":{
      "mbean":"java.lang:type=Memory",
      "attribute":"NonHeapMemoryUsage",
      "type":"read"
    },
    "value":{
      "init":2555904,
      "committed":51380224,
      "max":-1,
      "used":49944048
    },
    "timestamp":1446129191,
    "status":200
  }
]`

const validMultiValueJSON = `
[
  {
    "request":{
      "mbean":"java.lang:type=Memory",
      "attribute":"HeapMemoryUsage",
      "type":"read"
    },
    "value":{
      "init":67108864,
      "committed":456130560,
      "max":477626368,
      "used":203288528
    },
    "timestamp":1446129191,
    "status":200
  }
]`

const validSingleValueJSON = `
[
  {
    "request":{
      "path":"used",
      "mbean":"java.lang:type=Memory",
      "attribute":"HeapMemoryUsage",
      "type":"read"
    },
    "value":209274376,
    "timestamp":1446129256,
    "status":200
  }
]`

const invalidJSON = "I don't think this is JSON"

const empty = ""

var Servers = []string{"ECS7:ydh@127.0.0.1:7016"} // []Server{Server{Name: "as1", Host: "127.0.0.1", Port: "8080"}}
var HeapMetric = Metric{Name: "heap_memory_usage",
	Mbean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"}
var UsedHeapMetric = Metric{Name: "heap_memory_usage",
	Mbean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"}
var NonHeapMetric = Metric{Name: "non_heap_memory_usage",
	Mbean: "java.lang:type=Memory", Attribute: "NonHeapMemoryUsage"}

type jolokiaClientStub struct {
	responseBody string
	statusCode   int
}

func (c jolokiaClientStub) MakeRequest(req *http.Request) (*http.Response, error) {
	resp := http.Response{}
	resp.StatusCode = c.statusCode
	resp.Body = ioutil.NopCloser(strings.NewReader(c.responseBody))
	return &resp, nil
}

// Generates a pointer to an HttpJson object that uses a mock HTTP client.
// Parameters:
//     response  : Body of the response that the mock HTTP client should return
//     statusCode: HTTP status code the mock HTTP client should return
//
// Returns:
//     *HttpJson: Pointer to an HttpJson object that uses the generated mock HTTP client
func genJolokiaClientStub(response string, statusCode int, servers []string, metrics []Metric) *Jolokia2 {
	return &Jolokia2{
		jClient:   jolokiaClientStub{responseBody: response, statusCode: statusCode},
		Context:   "/gm/jolokia",
		Servers:   servers,
		Metrics:   metrics,
		Delimiter: "_",
	}
}

// Test that the proper values are ignored or collected
func TestHttpJsonMultiValue(t *testing.T) {
	jolokia := genJolokiaClientStub(validMultiValueJSON, 200, Servers, []Metric{Metric{Name: "Memory",
		Mbean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage"}})

	var acc testutil.Accumulator
	err := acc.GatherError(jolokia.Gather)

	assert.Nil(t, err)
	assert.Equal(t, 1, len(acc.Metrics))

	fields := map[string]interface{}{
		"init":      67108864.0,
		"committed": 456130560.0,
		"max":       477626368.0,
		"used":      203288528.0,
	}
	tags := map[string]string{
		"HostName": "ECS7",
		"AppName":  "ydh",
		"URI":      "127.0.0.1:7016",
	}

	acc.AssertContainsTaggedFields(t, "Memory", fields, tags)
}

// Test that bulk responses are handled
func TestHttpJsonBulkResponse(t *testing.T) {
	jolokia := genJolokiaClientStub(validBulkResponseJSON, 200, Servers, []Metric{HeapMetric, NonHeapMetric})
	var acc testutil.Accumulator
	err := jolokia.Gather(&acc)

	assert.Nil(t, err)
	assert.Equal(t, 2, len(acc.Metrics))
	fields := []map[string]interface{}{
		map[string]interface{}{
			"init":      67108864.0,
			"committed": 456130560.0,
			"max":       477626368.0,
			"used":      203288528.0,
		},
		map[string]interface{}{
			"init":      2555904.0,
			"committed": 51380224.0,
			"max":       -1.0,
			"used":      49944048.0,
		},
	}
	tags := []map[string]string{
		map[string]string{
			"HostName": "ECS7",
			"AppName":  "ydh",
			"URI":      "127.0.0.1:7016",
		}, map[string]string{
			"HostName": "ECS7",
			"AppName":  "ydh",
			"URI":      "127.0.0.1:7016",
		},
	}
	AssertMutiContainsTaggedFields(t, acc, []string{HeapMetric.Name, NonHeapMetric.Name}, fields, tags)
}

// Test that the proper values are ignored or collected
func TestHttpJsonThreeLevelMultiValue(t *testing.T) {
	jolokia := genJolokiaClientStub(validThreeLevelMultiValueJSON, 200, Servers, []Metric{HeapMetric})

	var acc testutil.Accumulator
	err := acc.GatherError(jolokia.Gather)

	assert.Nil(t, err)
	assert.Equal(t, 1, len(acc.Metrics))

	fields := map[string]interface{}{
		"java.lang:type=Memory_ObjectPendingFinalizationCount": 0.0,
		"java.lang:type=Memory_Verbose":                        false,
		"java.lang:type=Memory_HeapMemoryUsage_init":           134217728.0,
		"java.lang:type=Memory_HeapMemoryUsage_max":            1908932608.0,
		"java.lang:type=Memory_HeapMemoryUsage_used":           16840016.0,
		"java.lang:type=Memory_HeapMemoryUsage_committed":      173015040.0,
		"java.lang:type=Memory_NonHeapMemoryUsage_init":        2555904.0,
		"java.lang:type=Memory_NonHeapMemoryUsage_committed":   51380224.0,
		"java.lang:type=Memory_NonHeapMemoryUsage_max":         -1.0,
		"java.lang:type=Memory_NonHeapMemoryUsage_used":        49944048.0,
		"java.lang:type=Memory_ObjectName_objectName":          "java.lang:type=Memory",
	}

	tags := map[string]string{
		"HostName": "ECS7",
		"AppName":  "ydh",
		"URI":      "127.0.0.1:7016",
	}
	acc.AssertContainsTaggedFields(t, HeapMetric.Name, fields, tags)
}

// Test that the proper values are ignored or collected
func TestHttp404(t *testing.T) {

	jolokia := genJolokiaClientStub(invalidJSON, 404, Servers,
		[]Metric{UsedHeapMetric})

	var acc testutil.Accumulator
	acc.SetDebug(true)
	err := acc.GatherError(jolokia.Gather)

	assert.Error(t, err)
	assert.Equal(t, 0, len(acc.Metrics))
	assert.Contains(t, err.Error(), "has status code 404")
}

// Test that the proper values are ignored or collected
func TestHttpInvalidJson(t *testing.T) {

	jolokia := genJolokiaClientStub(invalidJSON, 200, Servers,
		[]Metric{UsedHeapMetric})

	var acc testutil.Accumulator
	acc.SetDebug(true)
	err := acc.GatherError(jolokia.Gather)

	assert.Error(t, err)
	assert.Equal(t, 0, len(acc.Metrics))
	assert.Contains(t, err.Error(), "Error decoding JSON response")
}

func AssertMutiContainsTaggedFields(
	t *testing.T,
	a testutil.Accumulator,
	measurement []string,
	fields []map[string]interface{},
	tags []map[string]string,
) {
	a.Lock()
	defer a.Unlock()
	for i, p := range a.Metrics {
		fmt.Println("aaa", p.Tags)
		if !reflect.DeepEqual(tags[i], p.Tags) {
			return
		}

		if p.Measurement == measurement[i] {
			assert.Equal(t, fields[i], p.Fields)
			return
		}
	}
	msg := fmt.Sprintf("unknown measurement %s", measurement)
	assert.Fail(t, msg)
}
