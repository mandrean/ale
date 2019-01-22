package jenkins

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/alde/ale/config"
	"github.com/kardianos/osext"
)

// CrawlJenkins initiates the crawler
func CrawlJenkins(conf *config.Config, buildURI string, buildID string) {
	uri0 := strings.Join([]string{strings.TrimRight(buildURI, "/"), "wfapi", "describe"}, "/")
	uri, _ := url.Parse(uri0)
	processChan := make(chan string, 1)
	stateChan := make(chan *JenkinsData, 1)
	go updateState(stateChan, processChan, conf, buildID)
	go crawlBuild(processChan, stateChan, uri)
	processChan <- buildID
}

func updateState(stateChan <-chan *JenkinsData, processChan chan<- string, conf *config.Config, buildID string) {
	for {
		select {
		case jdata := <-stateChan:
			logrus.Debug("got request to update the state")
			b, _ := json.MarshalIndent(jdata, "", "\t")
			folder, _ := osext.ExecutableFolder()
			file := fmt.Sprintf("%s/out_%s.json", folder, buildID)
			err := ioutil.WriteFile(file, b, 0644)
			if err != nil {
				logrus.Error(err)
			}
			logrus.WithField("file", file).Debug("file written")
			logrus.WithField("status", jdata.Status).Debug("jenkins job status")
			if jdata.Status == "" || jdata.Status == "IN_PROGRESS" {
				go func() {
					logrus.Debug("sleeping for 5 seconds before requerying")
					time.Sleep(5 * time.Second)
					processChan <- buildID
				}()
			}
		}
	}
}

func crawlBuild(processChan <-chan string, stateChan chan<- *JenkinsData, uri *url.URL) {
	for {
		select {
		case buildID := <-processChan:
			jd := &JobData{}
			resp, err := http.Get(uri.String())
			if err != nil {
				logrus.Error(err)
			}
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			err = json.Unmarshal(body, &jd)
			logrus.WithField("uri", uri.String()).Info("crawling jenkins API")
			jdata := extractLogs(jd, buildID, uri)
			logrus.Info("extracted jenkins data")
			stateChan <- jdata
			logrus.Debug("sent data to stateChannel")
		}
	}

}

func crawlJobStage(buildURL *url.URL, link string) JobExecution {
	stageLink := &url.URL{
		Scheme: buildURL.Scheme,
		Host:   buildURL.Host,
		Path:   link,
	}
	resp, err := http.Get(stageLink.String())
	if err != nil {
		logrus.Error(err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	var JobExecution JobExecution
	err = json.Unmarshal(body, &JobExecution)
	if err != nil {
		logrus.Error(err)
	}
	return JobExecution
}

func crawlExecutionLogs(execution *JobExecution, buildURL *url.URL) []*JenkinsStage {
	logLink := &url.URL{
		Scheme: buildURL.Scheme,
		Host:   buildURL.Host,
		Path:   execution.Links.Log.Href,
	}
	nodeLog := extractNodeLogs(logLink)
	return []*JenkinsStage{
		&JenkinsStage{
			Status:    nodeLog.NodeStatus,
			Name:      execution.Name,
			LogLength: nodeLog.Length,
			LogText:   nodeLog.Text,
			StartTime: execution.StartTimeMillis,
		},
	}
}

func extractLogsFromFlowNode(node *StageFlowNode, buildURL *url.URL, ename string) *JenkinsStage {
	logLink := &url.URL{
		Scheme: buildURL.Scheme,
		Host:   buildURL.Host,
		Path:   node.Links.Log.Href,
	}
	nodeLog := extractNodeLogs(logLink)
	return &JenkinsStage{
		Status:    nodeLog.NodeStatus,
		Name:      fmt.Sprintf("%s - %s", ename, node.Name),
		LogLength: nodeLog.Length,
		LogText:   nodeLog.Text,
		StartTime: node.StartTimeMillis,
	}
}

func extractNodeLogs(logLink *url.URL) *NodeLog {
	logrus.WithField("uri", logLink.String()).Info("crawling jenkins API")
	resp, err := http.Get(logLink.String())
	if err != nil {
		logrus.Error(err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	var nodeLog NodeLog
	err = json.Unmarshal(body, &nodeLog)
	if err != nil {
		logrus.Error(err)
	}
	return &nodeLog
}

func crawlStageFlowNodesLogs(execution *JobExecution, buildURL *url.URL) []*JenkinsStage {
	logs := []*JenkinsStage{}
	for _, node := range execution.StageFlowNodes {
		if node.Links.Log.Href == "" {
			continue
		}
		logs = append(logs, extractLogsFromFlowNode(&node, buildURL, execution.Name))
	}
	return logs
}

func extractLogsFromExecution(execution *JobExecution, buildURL *url.URL) []*JenkinsStage {
	if execution.Links.Log.Href == "" {
		return crawlStageFlowNodesLogs(execution, buildURL)
	}
	return crawlExecutionLogs(execution, buildURL)
}

func extractLogs(jd *JobData, buildID string, buildURL *url.URL) *JenkinsData {
	var stages []*JenkinsStage
	for _, stage := range jd.Stages {
		execution := crawlJobStage(buildURL, stage.Links.Self.Href)
		stages = append(stages, extractLogsFromExecution(&execution, buildURL)...)
	}

	sort.Slice(stages[:], func(i, j int) bool {
		return stages[i].StartTime < stages[j].StartTime
	})

	return &JenkinsData{
		Status:  jd.Status,
		Name:    jd.Name,
		ID:      jd.ID,
		BuildID: buildID,
		Stages:  stages,
	}
}
