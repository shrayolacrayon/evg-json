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
func getTasksForVersion(w http.ResponseWriter, r *http.Request) {
	var jsonForTasks []TaskJSON
	err := db.FindAllQ(collection, db.Query(bson.M{VersionIdKey: mux.Vars(r)["version_id"],
		NameKey: mux.Vars(r)["name"]}), &jsonForTasks)
	if err != nil {
		if err != mgo.ErrNotFound {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "{}", http.StatusNotFound)
		return
	}
	if jsonForTasks != nil {
		plugin.WriteJSON(w, http.StatusOK, jsonForTasks)
		return
	}
	plugin.WriteJSON(w, http.StatusNotFound, []TaskJSON{})
}
