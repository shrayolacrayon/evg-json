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
	"io/ioutil"
	"labix.org/v2/mgo"
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
	Name                string                 `bson:"name" json:"name"`
	TaskName            string                 `bson:"task_name" json:"task_name"`
	ProjectId           string                 `bson:"project_id" json:"project_id"`
	TaskId              string                 `bson:"task_id" json:"task_id"`
	BuildId             string                 `bson:"build_id" json:"build_id"`
	Variant             string                 `bson:"variant" json:"variant"`
	VersionId           string                 `bson:"version_id" json:"version_id"`
	RevisionOrderNumber int                    `bson:"order" json:"order"`
	Revision            string                 `bson:"revision" json:"revision"`
	Data                map[string]interface{} `bson:"data" json:"data"`
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
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonBlob := TaskJSON{
			TaskId:              task.Id,
			TaskName:            task.DisplayName,
			Name:                name,
			BuildId:             task.BuildId,
			Variant:             task.BuildVariant,
			ProjectId:           task.Project,
			VersionId:           task.Version,
			Revision:            task.Revision,
			RevisionOrderNumber: task.RevisionOrderNumber,
			Data:                rawData,
		}
		_, err = db.Upsert(collection, bson.M{"task_id": task.Id}, jsonBlob)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, "ok")
		return
	})

	r.HandleFunc("/data/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("fetching task")
		task := plugin.GetTask(r)
		if task == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		name := mux.Vars(r)["name"]
		taskName := mux.Vars(r)["task_name"]
		var jsonForTask TaskJSON
		fmt.Println("running query")
		q := bson.M{"version_id": task.Version, "build_id": task.BuildId, "name": name, "task_name": taskName}
		fmt.Println("query is", q)
		err := db.FindOneQ(collection, db.Query(bson.M{"version_id": task.Version, "build_id": task.BuildId, "name": name, "task_name": taskName}), &jsonForTask)
		if err != nil {
			fmt.Println("query err is not nill")
			if err == mgo.ErrNotFound {
				plugin.WriteJSON(w, http.StatusNotFound, nil)
				return
			}
			fmt.Println("err is", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Println("writing respons", jsonForTask.Data)
		plugin.WriteJSON(w, http.StatusOK, jsonForTask.Data)
	})
	return r
}

func (hwp *JSONPlugin) GetUIHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		plugin.WriteJSON(w, http.StatusOK, "1")
	})
	r.HandleFunc("/task/{task_id}/{name}/", func(w http.ResponseWriter, r *http.Request) {
		var jsonForTask TaskJSON
		err := db.FindOneQ(collection, db.Query(bson.M{"task_id": mux.Vars(r)["task_id"], "name": mux.Vars(r)["name"]}), &jsonForTask)
		if err != nil {
			if err != mgo.ErrNotFound {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, jsonForTask)
	})
	r.HandleFunc("/commit/{project_id}/{revision}/{variant}/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var jsonForTask TaskJSON
		err := db.FindOneQ(collection,
			db.Query(bson.M{"project_id": mux.Vars(r)["project_id"],
				"revision":  mux.Vars(r)["revision"],
				"variant":   mux.Vars(r)["variant"],
				"task_name": mux.Vars(r)["task_name"],
				"name":      mux.Vars(r)["name"],
			}), &jsonForTask)
		if err != nil {
			if err != mgo.ErrNotFound {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, jsonForTask)
	})
	r.HandleFunc("/history/{task_id}/{name}", func(w http.ResponseWriter, r *http.Request) {
		t, err := model.FindTask(mux.Vars(r)["task_id"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}

		before := []TaskJSON{}
		jsonQuery := db.Query(bson.M{
			"project_id": t.Project,
			"variant":    t.BuildVariant,
			"order":      bson.M{"$lte": t.RevisionOrderNumber},
			"task_name":  t.DisplayName,
			"name":       mux.Vars(r)["name"]})
		jsonQuery = jsonQuery.Sort([]string{"-order"}).Limit(200)
		err = db.FindAllQ(collection, jsonQuery, &before)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		//reverse order of "before" because we had to sort it backwards to apply the limit correctly:
		for i, j := 0, len(before)-1; i < j; i, j = i+1, j-1 {
			before[i], before[j] = before[j], before[i]
		}

		after := []TaskJSON{}
		jsonAfterQuery := db.Query(bson.M{
			"project_id": t.Project,
			"variant":    t.BuildVariant,
			"order":      bson.M{"$gt": t.RevisionOrderNumber},
			"task_name":  t.DisplayName,
			"name":       mux.Vars(r)["name"]}).Sort([]string{"order"}).Limit(100)
		err = db.FindAllQ(collection, jsonAfterQuery, &after)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		//concatenate before + after
		before = append(before, after...)
		plugin.WriteJSON(w, http.StatusOK, before)
	})
	return r
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
	} else if cmdName == "get" {
		return &JSONGetCommand{}, nil
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
	if jsc.File == "" {
		return fmt.Errorf("'file' param must not be blank")
	}
	if jsc.DataName == "" {
		return fmt.Errorf("'name' param must not be blank")
	}

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
		if err != nil {
			pluginLogger.LogTask(slogger.ERROR, "Sending json data failed: %v", err)
		}
		return err
	case <-stop:
		pluginLogger.LogExecution(slogger.INFO, "Received abort signal, stopping.")
		return nil
	}
}

type JSONGetCommand struct {
	File     string `mapstructure:"file" plugin:"expand"`
	DataName string `mapstructure:"name" plugin:"expand"`
	TaskName string `mapstructure:"task" plugin:"expand"`
}

func (jgc *JSONGetCommand) Name() string {
	return "get"
}

func (jgc *JSONGetCommand) Plugin() string {
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

func (jgc *JSONGetCommand) Execute(pluginLogger plugin.Logger,
	pluginCom plugin.PluginCommunicator,
	taskConfig *model.TaskConfig,
	stop chan bool) error {

	err := plugin.ExpandValues(jgc, taskConfig.Expansions)
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
		jgc.File = filepath.Join(taskConfig.WorkDir, jgc.File)
	}

	retriableGet := util.RetriableFunc(
		func() error {
			resp, err := pluginCom.TaskGetJSON(fmt.Sprintf("data/%s/%s", jgc.TaskName, jgc.DataName))
			if resp != nil {
				defer resp.Body.Close()
			}
			if err != nil {
				//Some generic error trying to connect - try again
				pluginLogger.LogExecution(slogger.WARN, "Error connecting to API server: %v", err)
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
