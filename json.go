package evgjson

import (
	"fmt"
	"github.com/10gen-labs/slogger/v1"
	"github.com/evergreen-ci/evergreen"
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/plugin"
	"github.com/evergreen-ci/evergreen/util"
	"github.com/gorilla/mux"
	"github.com/mitchellh/mapstructure"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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

// GetRoutes returns an API route for serving patch data.
func (jsp *JSONPlugin) GetAPIHandler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/tags/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		t := plugin.GetTask(r)
		if t == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		tagged := []TaskJSON{}
		jsonQuery := db.Query(bson.M{
			"project_id": t.Project,
			"variant":    t.BuildVariant,
			"task_name":  mux.Vars(r)["task_name"],
			"tag":        bson.M{"$exists": true, "$ne": ""},
			"name":       mux.Vars(r)["name"]})
		err := db.FindAllQ(collection, jsonQuery, &tagged)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, tagged)
	})
	r.HandleFunc("/history/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		t := plugin.GetTask(r)
		if t == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}

		var t2 *model.Task = t
		var err error

		if t.Requester == evergreen.PatchVersionRequester {
			t2, err = t.FindTaskOnBaseCommit()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			t.RevisionOrderNumber = t2.RevisionOrderNumber
		}

		before := []TaskJSON{}
		jsonQuery := db.Query(bson.M{
			"project_id": t.Project,
			"variant":    t.BuildVariant,
			"order":      bson.M{"$lte": t.RevisionOrderNumber},
			"task_name":  mux.Vars(r)["task_name"],
			"is_patch":   false,
			"name":       mux.Vars(r)["name"]}).Sort([]string{"-order"}).Limit(100)
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
			"task_name":  mux.Vars(r)["task_name"],
			"is_patch":   false,
			"name":       mux.Vars(r)["name"]}).Sort([]string{"order"}).Limit(100)
		err = db.FindAllQ(collection, jsonAfterQuery, &after)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		//concatenate before + after
		before = append(before, after...)

		// if our task was a patch, replace the base commit's info in the history with the patch
		if t.Requester == evergreen.PatchVersionRequester {
			var jsonForTask TaskJSON
			err := db.FindOneQ(collection, db.Query(bson.M{"task_id": t.Id}), &jsonForTask)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			found := false
			for i, item := range before {
				if item.Revision == t.Revision {
					before[i] = jsonForTask
					found = true
				}
			}
			// if found is false, it means we don't have json on the base commit, so it was
			// not replaced and we must add it explicitly
			if !found {
				before = append(before, jsonForTask)
			}
		}
		plugin.WriteJSON(w, http.StatusOK, before)
	})

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
			CreateTime:          task.CreateTime,
			Revision:            task.Revision,
			RevisionOrderNumber: task.RevisionOrderNumber,
			Data:                rawData,
			IsPatch:             task.Requester == evergreen.PatchVersionRequester,
		}
		_, err = db.Upsert(collection, bson.M{"task_id": task.Id, "name": name}, jsonBlob)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, "ok")
		return
	})

	r.HandleFunc("/data/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		task := plugin.GetTask(r)
		if task == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		name := mux.Vars(r)["name"]
		taskName := mux.Vars(r)["task_name"]

		var jsonForTask TaskJSON
		err := db.FindOneQ(collection, db.Query(bson.M{"version_id": task.Version, "build_id": task.BuildId, "name": name, "task_name": taskName}), &jsonForTask)
		if err != nil {
			if err == mgo.ErrNotFound {
				plugin.WriteJSON(w, http.StatusNotFound, nil)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(r.FormValue("full")) != 0 { // if specified, include the json data's container as well
			plugin.WriteJSON(w, http.StatusOK, jsonForTask)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, jsonForTask.Data)
	})
	r.HandleFunc("/data/{task_name}/{name}/{variant}", func(w http.ResponseWriter, r *http.Request) {
		task := plugin.GetTask(r)
		if task == nil {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		name := mux.Vars(r)["name"]
		taskName := mux.Vars(r)["task_name"]
		variantId := mux.Vars(r)["variant"]
		// Find the task for the other variant, if it exists
		ts, err := model.FindTasks(db.Query(bson.M{model.TaskVersionKey: task.Version, model.TaskBuildVariantKey: variantId, model.TaskDisplayNameKey: taskName}).Limit(1))
		if err != nil {
			if err == mgo.ErrNotFound {
				plugin.WriteJSON(w, http.StatusNotFound, nil)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(ts) == 0 {
			plugin.WriteJSON(w, http.StatusNotFound, nil)
			return
		}
		otherVariantTask := ts[0]

		var jsonForTask TaskJSON
		err = db.FindOneQ(collection, db.Query(bson.M{"task_id": otherVariantTask.Id, "name": name}), &jsonForTask)
		if err != nil {
			if err == mgo.ErrNotFound {
				plugin.WriteJSON(w, http.StatusNotFound, nil)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(r.FormValue("full")) != 0 { // if specified, include the json data's container as well
			plugin.WriteJSON(w, http.StatusOK, jsonForTask)
			return
		}
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

	r.HandleFunc("/task/{task_id}/{name}/tags", func(w http.ResponseWriter, r *http.Request) {
		t, err := model.FindTask(mux.Vars(r)["task_id"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tags := []struct {
			Tag string `bson:"_id" json:"tag"`
		}{}
		err = db.Aggregate(collection, []bson.M{
			{"$match": bson.M{"project_id": t.Project, "tag": bson.M{"$exists": true, "$ne": ""}}},
			{"$project": bson.M{"tag": 1}}, bson.M{"$group": bson.M{"_id": "$tag"}},
		}, &tags)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, tags)
	})
	r.HandleFunc("/task/{task_id}/{name}/tag", func(w http.ResponseWriter, r *http.Request) {
		t, err := model.FindTask(mux.Vars(r)["task_id"])
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if t == nil {
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		if r.Method == "DELETE" {
			if _, err = db.UpdateAll(collection,
				bson.M{"version_id": t.Version, "name": mux.Vars(r)["name"]},
				bson.M{"$unset": bson.M{"tag": 1}}); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			plugin.WriteJSON(w, http.StatusOK, "")
		}
		inTag := struct {
			Tag string `json:"tag"`
		}{}
		err = util.ReadJSONInto(r.Body, &inTag)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(inTag.Tag) == 0 {
			http.Error(w, "tag must not be blank", http.StatusBadRequest)
			return
		}

		_, err = db.UpdateAll(collection,
			bson.M{"version_id": t.Version, "name": mux.Vars(r)["name"]},
			bson.M{"$set": bson.M{"tag": inTag.Tag}})

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, "")
	})
	r.HandleFunc("/tag/{project_id}/{tag}/{variant}/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var jsonForTask TaskJSON
		err := db.FindOneQ(collection,
			db.Query(bson.M{"project_id": mux.Vars(r)["project_id"],
				"tag":       mux.Vars(r)["tag"],
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
		if len(r.FormValue("full")) != 0 { // if specified, include the json data's container as well
			plugin.WriteJSON(w, http.StatusOK, jsonForTask)
			return
		}
		plugin.WriteJSON(w, http.StatusOK, jsonForTask)
	})
	r.HandleFunc("/commit/{project_id}/{revision}/{variant}/{task_name}/{name}", func(w http.ResponseWriter, r *http.Request) {
		var jsonForTask TaskJSON
		err := db.FindOneQ(collection,
			db.Query(bson.M{"project_id": mux.Vars(r)["project_id"],
				"revision":  bson.RegEx{"^" + regexp.QuoteMeta(mux.Vars(r)["revision"]), "i"},
				"variant":   mux.Vars(r)["variant"],
				"task_name": mux.Vars(r)["task_name"],
				"name":      mux.Vars(r)["name"],
				"is_patch":  false,
			}), &jsonForTask)
		if err != nil {
			if err != mgo.ErrNotFound {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			http.Error(w, "{}", http.StatusNotFound)
			return
		}
		if len(r.FormValue("full")) != 0 { // if specified, include the json data's container as well
			plugin.WriteJSON(w, http.StatusOK, jsonForTask)
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
			"is_patch":   false,
			"name":       mux.Vars(r)["name"]})
		jsonQuery = jsonQuery.Sort([]string{"-order"}).Limit(100)
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
			"is_patch":   false,
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
