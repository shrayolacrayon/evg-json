package evgjson

import (
	"github.com/evergreen-ci/evergreen/db"
	"github.com/evergreen-ci/evergreen/plugin"
	"github.com/gorilla/mux"
	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"
	"net/http"
)

// getVersion returns a StatusOK if the route is hit
func getVersion(w http.ResponseWriter, r *http.Request) {
	plugin.WriteJSON(w, http.StatusOK, "1")
}

// getTasksForVersion sends back the list of TaskJSON documents associated with a version id.
func findTasksForVersion(versionId, name string) ([]TaskJSON, error) {
	var jsonForTasks []TaskJSON
	err := db.FindAllQ(collection, db.Query(bson.M{VersionIdKey: mux.Vars(r)["version_id"],
		NameKey: mux.Vars(r)["name"]}), &jsonForTasks)
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
		http.Error(w, http.StatusNotFound, []TaskJSON{})
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
	var jsonForTasks []TaskJSON
	project := mux.Vars(r)["project_id"]
	name := mux.Vars(r)["name"]

	jsonTask := db.FindOneQ(collection, db.Query(bson.M{NameKey: name,
		ProjectIdKey: project}).Sort([]string{"-" + RevisionOrderNumberKey}).WithFields(VersionIdKey))
	if jsonTask.VersionId == "" {
		http.Error(w, http.StatusNotFound, []TaskJSON{})
	}
	jsonTasks, err := findTasksForVersion(jsonTask.VersionId, name)
	if jsonForTasks == nil {
		http.Error(w, http.StatusNotFound, []TaskJSON{})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, jsonForTasks)
}
