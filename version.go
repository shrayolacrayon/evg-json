package evgjson

import (
	"net/http"
	"time"

	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/model/version"
	"github.com/evergreen-ci/evergreen/plugin"
	"github.com/gorilla/mux"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
)

type CommitInfo struct {
	Author     string    `json:"author"`
	Message    string    `json:"message"`
	CreateTime time.Time `json:"create_time"`
	Revision   string    `json:"revision"`
	VersionId  string    `json:"version_id"`
}

type VersionData struct {
	JSONTasks []TaskJSON `json:"json_tasks"`
	Commit    CommitInfo `json:"commit_info"`
}

// getVersion returns a StatusOK if the route is hit
func getVersion(w http.ResponseWriter, r *http.Request) {
	plugin.WriteJSON(w, http.StatusOK, "1")
}

// getTasksForVersion sends back the list of TaskJSON documents associated with a version id.
func findTasksForVersion(versionId, name string) ([]TaskJSON, error) {
	var jsonForTasks []TaskJSON
	err := db.FindAllQ(collection, db.Query(bson.M{VersionIdKey: versionId,
		NameKey: name}), &jsonForTasks)
	if err != nil {
		if err != mgo.ErrNotFound {
			return nil, err
		}
		return nil, err
	}

	return jsonForTasks, err
}

// getTasksForVersion sends back the list of TaskJSON documents associated with a version id.
func getTasksForVersion(w http.ResponseWriter, r *http.Request) {
	jsonForTasks, err := findTasksForVersion(mux.Vars(r)["version_id"], mux.Vars(r)["name"])
	if jsonForTasks == nil {
		http.Error(w, "{}", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, jsonForTasks)
	return
}

// getTasksForLatestVersion sends back the TaskJSON data associated with the latest version.
func getTasksForLatestVersion(w http.ResponseWriter, r *http.Request) {
	project := mux.Vars(r)["project_id"]
	name := mux.Vars(r)["name"]
	var jsonTask TaskJSON
	err := db.FindOneQ(collection, db.Query(bson.M{NameKey: name,
		ProjectIdKey: project}).Sort([]string{"-" + RevisionOrderNumberKey}).WithFields(VersionIdKey), &jsonTask)
	if err != nil {
		if err != mgo.ErrNotFound {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
		return
	}
	if jsonTask.VersionId == "" {
		http.Error(w, "{}", http.StatusNotFound)
	}
	jsonTasks, err := findTasksForVersion(jsonTask.VersionId, name)
	if jsonTasks == nil {
		http.Error(w, "{}", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// get the version commit info
	v, err := version.FindOne(version.ById(jsonTask.VersionId))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if v == nil {
		http.Error(w, "{}", http.StatusNotFound)
		return
	}

	commitInfo := CommitInfo{
		Author:     v.Author,
		Message:    v.Message,
		CreateTime: v.CreateTime,
		Revision:   v.Revision,
		VersionId:  jsonTask.VersionId,
	}

	data := VersionData{jsonTasks, commitInfo}
	plugin.WriteJSON(w, http.StatusOK, data)
}
