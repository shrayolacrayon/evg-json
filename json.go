package evgjson

import (
	"fmt"
	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/db/bsonutil"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/task"
	"github.com/evergreen-ci/evergreen/plugin"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/gorilla/mux"
	"github.com/mitchellh/mapstructure"
	"gopkg.in/mgo.v2/bson"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const collection = "json"

func init() {
	plugin.Publish(&JSONPlugin{})
}

// GitPlugin handles fetching source code and applying patches
// using the git version control system.
type JSONPlugin struct{}

// Name implements Plugin Interface.
func (jsp *JSONPlugin) Name() string {
	return "json"
}

type TaskJSON struct {
	Name                string                 `bson:"name" json:"name"`
	TaskName            string                 `bson:"task_name" json:"task_name"`
	ProjectId           string                 `bson:"project_id" json:"project_id"`
	TaskId              string                 `bson:"task_id" json:"task_id"`
	BuildId             string                 `bson:"build_id" json:"build_id"`
	Variant             string                 `bson:"variant" json:"variant"`
	VersionId           string                 `bson:"version_id" json:"version_id"`
	CreateTime          time.Time              `bson:"create_time" json:"create_time"`
	IsPatch             bool                   `bson:"is_patch" json:"is_patch"`
	RevisionOrderNumber int                    `bson:"order" json:"order"`
	Revision            string                 `bson:"revision" json:"revision"`
	Data                map[string]interface{} `bson:"data" json:"data"`
	Tag                 string                 `bson:"tag" json:"tag"`
}

var (
	// BSON fields for the TaskJSON struct
	NameKey                = bsonutil.MustHaveTag(TaskJSON{}, "Name")
	TaskNameKey            = bsonutil.MustHaveTag(TaskJSON{}, "TaskName")
	ProjectIdKey           = bsonutil.MustHaveTag(TaskJSON{}, "ProjectId")
	TaskIdKey              = bsonutil.MustHaveTag(TaskJSON{}, "TaskId")
	BuildIdKey             = bsonutil.MustHaveTag(TaskJSON{}, "BuildId")
	VariantKey             = bsonutil.MustHaveTag(TaskJSON{}, "Variant")
	VersionIdKey           = bsonutil.MustHaveTag(TaskJSON{}, "VersionId")
	CreateTimeKey          = bsonutil.MustHaveTag(TaskJSON{}, "CreateTime")
	IsPatchKey             = bsonutil.MustHaveTag(TaskJSON{}, "IsPatch")
	RevisionOrderNumberKey = bsonutil.MustHaveTag(TaskJSON{}, "RevisionOrderNumber")
	RevisionKey            = bsonutil.MustHaveTag(TaskJSON{}, "Revision")
	DataKey                = bsonutil.MustHaveTag(TaskJSON{}, "Data")
	TagKey                 = bsonutil.MustHaveTag(TaskJSON{}, "Tag")
)

// GetRoutes returns an API route for serving patch data.
func (jsp *JSONPlugin) GetAPIHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/tags/{task_name}/{name}", getTaskByTag)
	r.HandleFunc("/history/{task_name}/{name}", apiGetTaskHistory)

	r.HandleFunc("/data/{name}", insertTask)
	r.HandleFunc("/data/{task_name}/{name}", getTaskByName)
	r.HandleFunc("/data/{task_name}/{name}/{variant}", getTaskForVariant)
	return r
}

func (hwp *JSONPlugin) GetUIHandler() http.Handler {
	r := mux.NewRouter()

	// version routes
	r.HandleFunc("/version", getVersion)
	r.HandleFunc("/version/{version_id}/{name}/", getTasksForVersion)

	// task routes
	r.HandleFunc("/task/{task_id}/{name}/", getTaskById)
	r.HandleFunc("/task/{task_id}/{name}/tags", getTags)
	r.HandleFunc("/task/{task_id}/{name}/tag", handleTaskTag)

	r.HandleFunc("/tag/{project_id}/{tag}/{variant}/{task_name}/{name}", getTaskJSONByTag)
	r.HandleFunc("/commit/{project_id}/{revision}/{variant}/{task_name}/{name}", getCommit)
	r.HandleFunc("/history/{task_id}/{name}}", uiGetTaskHistory)
	return r
}

func fixPatchInHistory(taskId string, base *task.Task, history []TaskJSON) ([]TaskJSON, error) {
	var jsonForTask *TaskJSON
	err := db.FindOneQ(collection, db.Query(bson.M{"task_id": taskId}), &jsonForTask)
	if err != nil {
		return nil, err
	}
	if base != nil {
		jsonForTask.RevisionOrderNumber = base.RevisionOrderNumber
	}
	if jsonForTask == nil {
		return history, nil
	}

	found := false
	for i, item := range history {
		if item.Revision == base.Revision {
			history[i] = *jsonForTask
			found = true
		}
	}
	// if found is false, it means we don't have json on the base commit, so it was
	// not replaced and we must add it explicitly
	if !found {
		history = append(history, *jsonForTask)
	}
	return history, nil
}

func (jsp *JSONPlugin) Configure(map[string]interface{}) error {
	return nil
}

// GetPanelConfig is required to fulfill the Plugin interface. This plugin
// does not have any UI hooks.
func (jsp *JSONPlugin) GetPanelConfig() (*plugin.PanelConfig, error) {
	return &plugin.PanelConfig{
		Panels: []plugin.UIPanel{
			{
				Page:      plugin.TaskPage,
				Position:  plugin.PageCenter,
				PanelHTML: "<!--hello world!-->",
				DataFunc: func(context plugin.UIContext) (interface{}, error) {
					return map[string]interface{}{}, nil
				},
			},
		},
	}, nil

	return nil, nil
}

// NewCommand returns requested commands by name. Fulfills the Plugin interface.
func (jsp *JSONPlugin) NewCommand(cmdName string) (plugin.Command, error) {
	if cmdName == "send" {
		return &JSONSendCommand{}, nil
	} else if cmdName == "get" {
		return &JSONGetCommand{}, nil
	} else if cmdName == "get_history" {
		return &JSONHistoryCommand{}, nil
	}
	return nil, &plugin.ErrUnknownCommand{cmdName}
}

type JSONSendCommand struct {
	File     string `mapstructure:"file" plugin:"expand"`
	DataName string `mapstructure:"name" plugin:"expand"`
}

func (jsc *JSONSendCommand) Name() string {
	return "send"
}

func (jsc *JSONSendCommand) Plugin() string {
	return "json"
}

func (jsc *JSONSendCommand) ParseParams(params map[string]interface{}) error {
	if err := mapstructure.Decode(params, jsc); err != nil {
		return fmt.Errorf("error decoding '%v' params: %v", jsc.Name(), err)
	}
	return nil
}

func (jsc *JSONSendCommand) Execute(log plugin.Logger, com plugin.PluginCommunicator, conf *model.TaskConfig, stop chan bool) error {
	if jsc.File == "" {
		return fmt.Errorf("'file' param must not be blank")
	}
	if jsc.DataName == "" {
		return fmt.Errorf("'name' param must not be blank")
	}

	errChan := make(chan error)
	go func() {
		// attempt to open the file
		fileLoc := filepath.Join(conf.WorkDir, jsc.File)
		jsonFile, err := os.Open(fileLoc)
		if err != nil {
			errChan <- fmt.Errorf("Couldn't open json file: '%v'", err)
			return
		}

		jsonData := map[string]interface{}{}
		err = util.ReadJSONInto(jsonFile, &jsonData)
		if err != nil {
			errChan <- fmt.Errorf("File contained invalid json: %v", err)
			return
		}

		retriablePost := util.RetriableFunc(
			func() error {
				log.LogTask(slogger.INFO, "Posting JSON")
				resp, err := com.TaskPostJSON(fmt.Sprintf("data/%v", jsc.DataName), jsonData)
				if resp != nil {
					defer resp.Body.Close()
				}
				if err != nil {
					return util.RetriableError{err}
				}
				if resp.StatusCode != http.StatusOK {
					return util.RetriableError{fmt.Errorf("unexpected status code %v", resp.StatusCode)}
				}
				return nil
			},
		)

		_, err = util.Retry(retriablePost, 10, 3*time.Second)
		errChan <- err
	}()

	select {
	case err := <-errChan:
		if err != nil {
			log.LogTask(slogger.ERROR, "Sending json data failed: %v", err)
		}
		return err
	case <-stop:
		log.LogExecution(slogger.INFO, "Received abort signal, stopping.")
		return nil
	}
}

type JSONGetCommand struct {
	File     string `mapstructure:"file" plugin:"expand"`
	DataName string `mapstructure:"name" plugin:"expand"`
	TaskName string `mapstructure:"task" plugin:"expand"`
	Variant  string `mapstructure:"variant" plugin:"expand"`
}

type JSONHistoryCommand struct {
	Tags     bool   `mapstructure:"tags"`
	File     string `mapstructure:"file" plugin:"expand"`
	DataName string `mapstructure:"name" plugin:"expand"`
	TaskName string `mapstructure:"task" plugin:"expand"`
}

func (jgc *JSONGetCommand) Name() string {
	return "get"
}

func (jgc *JSONHistoryCommand) Name() string {
	return "history"
}

func (jgc *JSONGetCommand) Plugin() string {
	return "json"
}

func (jgc *JSONHistoryCommand) Plugin() string {
	return "json"
}

func (jgc *JSONGetCommand) ParseParams(params map[string]interface{}) error {
	if err := mapstructure.Decode(params, jgc); err != nil {
		return fmt.Errorf("error decoding '%v' params: %v", jgc.Name(), err)
	}
	if jgc.File == "" {
		return fmt.Errorf("JSON 'get' command must not have blank 'file' parameter")
	}
	return nil
}

func (jgc *JSONHistoryCommand) ParseParams(params map[string]interface{}) error {
	if err := mapstructure.Decode(params, jgc); err != nil {
		return fmt.Errorf("error decoding '%v' params: %v", jgc.Name(), err)
	}
	if jgc.File == "" {
		return fmt.Errorf("JSON 'history' command must not have blank 'file' parameter")
	}
	return nil
}

func (jgc *JSONGetCommand) Execute(log plugin.Logger, com plugin.PluginCommunicator, conf *model.TaskConfig, stop chan bool) error {

	err := plugin.ExpandValues(jgc, conf.Expansions)
	if err != nil {
		return err
	}

	if jgc.File == "" {
		return fmt.Errorf("'file' param must not be blank")
	}
	if jgc.DataName == "" {
		return fmt.Errorf("'name' param must not be blank")
	}
	if jgc.TaskName == "" {
		return fmt.Errorf("'task' param must not be blank")
	}

	if jgc.File != "" && !filepath.IsAbs(jgc.File) {
		jgc.File = filepath.Join(conf.WorkDir, jgc.File)
	}

	retriableGet := util.RetriableFunc(
		func() error {
			dataUrl := fmt.Sprintf("data/%s/%s", jgc.TaskName, jgc.DataName)
			if jgc.Variant != "" {
				dataUrl = fmt.Sprintf("data/%s/%s/%s", jgc.TaskName, jgc.DataName, jgc.Variant)
			}
			resp, err := com.TaskGetJSON(dataUrl)
			if resp != nil {
				defer resp.Body.Close()
			}
			if err != nil {
				//Some generic error trying to connect - try again
				log.LogExecution(slogger.WARN, "Error connecting to API server: %v", err)
				return util.RetriableError{err}
			}

			if resp.StatusCode == http.StatusOK {
				jsonBytes, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				return ioutil.WriteFile(jgc.File, jsonBytes, 0755)
			}
			if resp.StatusCode != http.StatusOK {
				if resp.StatusCode == http.StatusNotFound {
					return fmt.Errorf("No JSON data found")
				}
				return util.RetriableError{fmt.Errorf("unexpected status code %v", resp.StatusCode)}
			}
			return nil
		},
	)
	_, err = util.Retry(retriableGet, 10, 3*time.Second)
	return err
}

func (jgc *JSONHistoryCommand) Execute(log plugin.Logger, com plugin.PluginCommunicator, conf *model.TaskConfig, stop chan bool) error {
	err := plugin.ExpandValues(jgc, conf.Expansions)
	if err != nil {
		return err
	}

	if jgc.File == "" {
		return fmt.Errorf("'file' param must not be blank")
	}
	if jgc.DataName == "" {
		return fmt.Errorf("'name' param must not be blank")
	}
	if jgc.TaskName == "" {
		return fmt.Errorf("'task' param must not be blank")
	}

	if jgc.File != "" && !filepath.IsAbs(jgc.File) {
		jgc.File = filepath.Join(conf.WorkDir, jgc.File)
	}

	endpoint := fmt.Sprintf("history/%s/%s", jgc.TaskName, jgc.DataName)
	if jgc.Tags {
		endpoint = fmt.Sprintf("tags/%s/%s", jgc.TaskName, jgc.DataName)
	}

	retriableGet := util.RetriableFunc(
		func() error {
			resp, err := com.TaskGetJSON(endpoint)
			if resp != nil {
				defer resp.Body.Close()
			}
			if err != nil {
				//Some generic error trying to connect - try again
				log.LogExecution(slogger.WARN, "Error connecting to API server: %v", err)
				return util.RetriableError{err}
			}

			if resp.StatusCode == http.StatusOK {
				jsonBytes, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					return err
				}
				return ioutil.WriteFile(jgc.File, jsonBytes, 0755)
			}
			if resp.StatusCode != http.StatusOK {
				if resp.StatusCode == http.StatusNotFound {
					return fmt.Errorf("No JSON data found")
				}
				return util.RetriableError{fmt.Errorf("unexpected status code %v", resp.StatusCode)}
			}
			return nil
		},
	)
	_, err = util.Retry(retriableGet, 10, 3*time.Second)
	return err
}
