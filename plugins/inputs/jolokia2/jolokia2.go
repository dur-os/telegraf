package jolokia2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// Default http timeouts
var DefaultResponseHeaderTimeout = internal.Duration{Duration: 3 * time.Second}
var DefaultClientTimeout = internal.Duration{Duration: 4 * time.Second}

var serverInfos []serverInfo

type serverInfo struct {
	HostName string
	AppName  string
	URI      string
	UserName string
	Password string
	Metrics  []Metric
}

type Metric struct {
	ServerName []string
	Name       string
	Mbean      string
	Attribute  string
	Path       string
	Tags       map[string]string
}

type JolokiaClient interface {
	MakeRequest(req *http.Request) (*http.Response, error)
}

type JolokiaClientImpl struct {
	client *http.Client
}

func (c JolokiaClientImpl) MakeRequest(req *http.Request) (*http.Response, error) {
	return c.client.Do(req)
}

type Jolokia2 struct {
	jClient   JolokiaClient
	Context   string
	Servers   []string //HostName:AppName@IP:PORT@USERNAME:PWD
	Metrics   []Metric
	Proxy     []string
	Delimiter string

	ResponseHeaderTimeout internal.Duration `toml:"response_header_timeout"`
	ClientTimeout         internal.Duration `toml:"client_timeout"`
}

const sampleConfig = `
  ## This is the context root used to compose the jolokia url
  ## NOTE that Jolokia requires a trailing slash at the end of the context root
  ## NOTE that your jolokia security policy must allow for POST requests.
  context = "/jolokia/"

  ## List of servers exposing jolokia read service
  Servers = HostName:AppName@IP:PORT@USERNAME:PWD
  
  ## Optional http timeouts
  ##
  ## response_header_timeout, if non-zero, specifies the amount of time to wait
  ## for a server's response headers after fully writing the request.
  # response_header_timeout = "3s"
  ##
  ## client_timeout specifies a time limit for requests made by this client.
  ## Includes connection time, any redirects, and reading the response body.
  # client_timeout = "4s"

  ## Attribute delimiter
  ##
  ## When multiple attributes are returned for a single
  ## [inputs.jolokia.metrics], the field name is a concatenation of the metric
  ## name, and the attribute name, separated by the given delimiter.
  # delimiter = "_"

  [[inputs.jolokia.metrics]]
    name = "heap_memory_usage"
    mbean  = "java.lang:type=Memory"
    attribute = "HeapMemoryUsage"

  ## This collect thread counts metrics.
  [[inputs.jolokia.metrics]]
    name = "thread_count"
    mbean  = "java.lang:type=Threading"
    attribute = "TotalStartedThreadCount,ThreadCount,DaemonThreadCount,PeakThreadCount"

  ## This collect number of class loaded/unloaded counts metrics.
  [[inputs.jolokia.metrics]]
    name = "class_count"
    mbean  = "java.lang:type=ClassLoading"
    attribute = "LoadedClassCount,UnloadedClassCount,TotalLoadedClassCount"
`

func (j *Jolokia2) SampleConfig() string {
	return sampleConfig
}

func (j *Jolokia2) Description() string {
	return "Read JMX metrics through Jolokia"
}

func (j *Jolokia2) doRequest(req *http.Request) ([]map[string]interface{}, error) {
	resp, err := j.jClient.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Process response
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("Response from url \"%s\" has status code %d (%s), expected %d (%s)",
			req.RequestURI,
			resp.StatusCode,
			http.StatusText(resp.StatusCode),
			http.StatusOK,
			http.StatusText(http.StatusOK))
		return nil, err
	}

	// read body
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Unmarshal json
	var jsonOut []map[string]interface{}
	if err = json.Unmarshal([]byte(body), &jsonOut); err != nil {
		return nil, fmt.Errorf("Error decoding JSON response: %s: %s", err, body)
	}

	return jsonOut, nil
}

func (j *Jolokia2) prepareRequest(server serverInfo, metrics []Metric) (*http.Request, error) {
	var jolokiaUrl *url.URL
	context := j.Context // Usually "/jolokia/"
	var bulkBodyContent []map[string]interface{}
	for _, metric := range metrics {
		// Create bodyContent
		bodyContent := map[string]interface{}{
			"type":  "read",
			"mbean": metric.Mbean,
		}

		if metric.Attribute != "" {
			bodyContent["attribute"] = metric.Attribute
			if metric.Path != "" {
				bodyContent["path"] = metric.Path
			}
		}
		serverUrl, err := url.Parse("http://" + server.URI + context)
		if err != nil {
			return nil, err
		}
		if server.UserName != "" || server.Password != "" {
			serverUrl.User = url.UserPassword(server.UserName, server.Password)
		}
		jolokiaUrl = serverUrl
		bulkBodyContent = append(bulkBodyContent, bodyContent)
	}

	requestBody, err := json.Marshal(bulkBodyContent)

	req, err := http.NewRequest("POST", jolokiaUrl.String(), bytes.NewBuffer(requestBody))

	if err != nil {
		return nil, err
	}

	req.Header.Add("Content-type", "application/json")

	return req, nil
}

func (j *Jolokia2) analysisURI(acc telegraf.Accumulator) {
	if serverInfos == nil || len(serverInfos) == 0 {
		for _, uri := range j.Servers {
			infos := strings.Split(uri, "@")
			if len(infos) < 2 {
				acc.AddError(fmt.Errorf("E! Server [%s], skipping", uri))
				continue
			}
			names := strings.Split(infos[0], ":")
			if len(names) != 2 {
				acc.AddError(fmt.Errorf("E! Server[HostName:AppName] [%s], skipping", infos[0]))
				continue
			}
			url := strings.Split(infos[1], ":")
			if len(url) != 2 || net.ParseIP(url[0]) == nil {
				acc.AddError(fmt.Errorf("E! Server[Host:Port] [%s], skipping", infos[1]))
				continue
			}
			if s, err := strconv.Atoi(url[1]); err != nil || s < 0 || s > 65535 {
				acc.AddError(fmt.Errorf("E! Server[Host:Port] [%s], skipping", infos[1]))
				continue
			}
			info := serverInfo{HostName: names[0], AppName: names[1], URI: infos[1]}
			if len(infos) > 2 {
				up := strings.Join(infos[2:], "@")
				ups := strings.Split(up, ":")
				info.UserName = ups[0]
				if len(ups) > 1 {
					info.Password = strings.Join(ups[1:], ":")
				}
			}
			serverInfos = append(serverInfos, info)
		}
		for _, metric := range j.Metrics {
			if metric.ServerName == nil || len(metric.ServerName) == 0 {
				j.addMetric("", "", metric)
			} else {
				for _, serverName := range metric.ServerName {
					si := strings.Split(serverName, "@")
					if len(si) == 1 {
						j.addMetric(si[0], "", metric)
					} else {
						j.addMetric(si[0], strings.Join(si[1:], "@"), metric)
					}
				}
			}
		}
	}
}

func (j *Jolokia2) addMetric(hostName, appName string, metric Metric) {
	for i, serverInfo := range serverInfos {
		if hostName == "" && appName == "" {
			serverInfos[i].Metrics = append(serverInfos[i].Metrics, metric)
		} else if hostName == "" && appName != "" && serverInfo.AppName == appName {
			serverInfos[i].Metrics = append(serverInfos[i].Metrics, metric)
		} else if hostName != "" && appName == "" && serverInfo.HostName == hostName {
			serverInfos[i].Metrics = append(serverInfos[i].Metrics, metric)
		} else if hostName != "" && appName != "" && serverInfo.HostName == hostName && serverInfo.AppName == appName {
			serverInfos[i].Metrics = append(serverInfos[i].Metrics, metric)
		}

	}
}

func (j *Jolokia2) extractValues(measurement string, value interface{}, fields map[string]interface{}) {
	if mapValues, ok := value.(map[string]interface{}); ok {
		for k2, v2 := range mapValues {
			if measurement != "" {
				j.extractValues(measurement+j.Delimiter+k2, v2, fields)
			} else {
				j.extractValues(k2, v2, fields)
			}

		}
	} else {
		fields[measurement] = value
	}
}

func (j *Jolokia2) Gather(acc telegraf.Accumulator) error {
	if j.jClient == nil {
		tr := &http.Transport{ResponseHeaderTimeout: j.ResponseHeaderTimeout.Duration}
		j.jClient = &JolokiaClientImpl{&http.Client{
			Transport: tr,
			Timeout:   j.ClientTimeout.Duration,
		}}
	}

	j.analysisURI(acc)
	for _, server := range serverInfos {
		tags := make(map[string]string)
		tags["HostName"] = server.HostName
		tags["AppName"] = server.AppName
		tags["URI"] = server.URI

		req, err := j.prepareRequest(server, server.Metrics)
		if err != nil {
			acc.AddError(fmt.Errorf("unable to create request: %s", err))
			continue
		}
		out, err := j.doRequest(req)
		if err != nil {
			acc.AddError(fmt.Errorf("error performing request: %s", err))
			continue
		}

		if len(out) != len(server.Metrics) {
			acc.AddError(fmt.Errorf("did not receive the correct number of metrics in response. expected %d, received %d", len(server.Metrics), len(out)))
			continue
		}

		for i, resp := range out {
			fields := make(map[string]interface{})
			if status, ok := resp["status"]; ok && status != float64(200) {
				acc.AddError(fmt.Errorf("Not expected status value in response body (%s mbean=\"%s\" attribute=\"%s\"): %3.f",
					server.URI, server.Metrics[i].Mbean, server.Metrics[i].Attribute, status))
				continue
			} else if !ok {
				acc.AddError(fmt.Errorf("Missing status in response body"))
				continue
			}

			if values, ok := resp["value"]; ok {
				j.extractValues("", values, fields)
			} else {
				acc.AddError(fmt.Errorf("Missing key 'value' in output response\n"))
			}

			if server.Metrics[i].Tags != nil {
				for key, val := range server.Metrics[i].Tags {
					tags[key] = val
				}
			}
			acc.AddFields(server.Metrics[i].Name, fields, tags)
		}
	}
	return nil
}

func init() {
	inputs.Add("jolokia2", func() telegraf.Input {
		return &Jolokia2{
			ResponseHeaderTimeout: DefaultResponseHeaderTimeout,
			ClientTimeout:         DefaultClientTimeout,
			Delimiter:             "_",
		}
	})
}
