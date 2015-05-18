package git

import (
	"fmt"
	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/plugin"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/gorilla/mux"
	"github.com/mitchellh/mapstructure"
	"labix.org/v2/mgo/bson"
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
	Name                string                 `bson:"name"`
	TaskId              string                 `bson:"task_id"`
	BuildId             string                 `bson:"build_id"`
	Variant             string                 `bson:"variant"`
	VersionId           string                 `bson:"version_id"`
	RevisionOrderNumber int                    `bson:"order"`
	Revision            string                 `bson:"revision"`
	Data                map[string]interface{} `bson:"data"`
}

// GetRoutes returns an API route for serving patch data.
func (jsp *JSONPlugin) GetAPIHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/data/{name}", func(w http.ResponseWriter, r *http.Request) {
		task := plugin.GetTask(r)
		if task == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		name := mux.Vars(r)["name"]
		rawData := map[string]interface{}{}
		err := util.ReadJSONInto(r.Body, &rawData)
		if err != nil {
			fmt.Println("error reading body", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonBlob := TaskJSON{
			TaskId:              task.Id,
			Name:                name,
			BuildId:             task.BuildId,
			Variant:             task.BuildVariant,
			VersionId:           task.Version,
			RevisionOrderNumber: task.RevisionOrderNumber,
			Data:                rawData,
		}
		_, err = db.Upsert(collection, bson.M{"task_id": task.Id}, jsonBlob)
		if err != nil {
			fmt.Println("error inserting", err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, "ok")
	})
	return r
}

func (hwp *JSONPlugin) GetUIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
}

func (jsp *JSONPlugin) Configure(map[string]interface{}) error {
	return nil
}

// GetPanelConfig is required to fulfill the Plugin interface. This plugin
// does not have any UI hooks.
func (jsp *JSONPlugin) GetPanelConfig() (*plugin.PanelConfig, error) {
	return &plugin.PanelConfig{
		StaticRoot: plugin.StaticWebRootFromSourceFile(),
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

func (jsc *JSONSendCommand) Execute(pluginLogger plugin.Logger,
	pluginCom plugin.PluginCommunicator,
	taskConfig *model.TaskConfig,
	stop chan bool) error {

	errChan := make(chan error)
	go func() {
		// attempt to open the file
		fileLoc := filepath.Join(taskConfig.WorkDir, jsc.File)
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
				pluginLogger.LogTask(slogger.INFO, "Posting JSON")
				resp, err := pluginCom.TaskPostJSON(fmt.Sprintf("data/%v", jsc.DataName), jsonData)
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
		pluginLogger.LogTask(slogger.ERROR, "Sending json data failed: %v", err)
		return err
	case <-stop:
		pluginLogger.LogExecution(slogger.INFO, "Received abort signal, stopping.")
		return nil
	}
}
