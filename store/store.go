/*
Copyright 2014 Rohith All rights reserved.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package store

import (
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/gambol99/config-fs/store/dynamic"
	"github.com/gambol99/config-fs/store/fs"
	"github.com/gambol99/config-fs/store/kv"
	"github.com/go-fsnotify/fsnotify"
	"github.com/golang/glog"
)

const (
	DEFAULT_MOUNT_POINT    = "/config"
	DEFAULT_DELETE_ON_EXIT = false
	DEFAULT_PRE_SYNC       = true
	DEFAULT_READ_ONLY      = true
	DEFAULT_INTERVAL       = 900
	DEFAULT_DYNAMIC_PREFIX = "$TEMPLATE$"
	VERBOSE_LEVEL          = 5
)

/* --- command line options ---- */
var (
	mount_point                                *string
	delete_on_exit, read_only, pre_synchronize *bool
	refresh_interval                           *int
)

func init() {
	mount_point = flag.String("mount", DEFAULT_MOUNT_POINT, "the mount point for the K/V store")
	delete_on_exit = flag.Bool("delete", DEFAULT_DELETE_ON_EXIT, "delete all configuration on exit")
	refresh_interval = flag.Int("interval", DEFAULT_INTERVAL, "the default interval for performed a forced resync")
	read_only = flag.Bool("readonly", DEFAULT_READ_ONLY, "wheather or not the config store of read-only")
	pre_synchronize = flag.Bool("sync", DEFAULT_PRE_SYNC, "wheather or not to perform a initial config sync against the backend")
}

/* The interface to the config-fs */
type Store interface {
	/* perform synchronization between the mount point and the kv store */
	Synchronize() error
	/* shutdown the resources */
	Close()
	/* delete the configuration directory */
	DeleteConfiguration() error
}

/* The implementation of the above */
type ConfigurationStore struct {
	/* the file system implementation */
	fs fs.FileStore
	/* the k/v agent for the store */
	kv kv.KVStore
	/* the templated resources */
	dynamic dynamic.DynamicStore

	/* the shutdown signal */
	shutdownChannel chan bool
	/* updates and changes to templated resourcs channel */
	dynamicEventChannel dynamic.DynamicUpdateChannel
	/* changes and updates to the file system channel */
	filesystemEventChannel WatchServiceChannel
	/* changes and uydates to the k/v store */
	nodeEventChannel kv.NodeUpdateChannel
	/* a timer channel */
	timerEventChannel *time.Ticker
}

/* Create a new configuration store */
func NewConfigurationStore() (Store, error) {
	glog.Infof("Creating a new configuration store, mountpoint: '%s'")
	/* step: we create the kv store */
	if kvstore, err := kv.NewKVStore(); err != nil {
		glog.Errorf("Failed to create the K/V Store, error: %s", err)
		return nil, err
	} else {
		/* step; create the configuration store */
		service := new(ConfigurationStore)
		service.fs = fs.NewStoreFS()
		service.kv = kvstore
		service.dynamic = dynamic.NewDynamicStore(DEFAULT_DYNAMIC_PREFIX, kvstore)
		service.shutdownChannel = make(chan bool, 1)
		service.nodeEventChannel = make(kv.NodeUpdateChannel, 10)
		service.dynamicEventChannel = make(dynamic.DynamicUpdateChannel, 10)
		service.filesystemEventChannel = make(WatchServiceChannel, 10)
		service.timerEventChannel = time.NewTicker(time.Duration(*refresh_interval) * time.Second)
		return service, nil
	}
}

func (r *ConfigurationStore) Close() {
	glog.Infof("Request to shutdown and release the resources")
	r.shutdownChannel <- true
}

/* Synchronize the key/value store with the configuration directory */
func (r *ConfigurationStore) Synchronize() error {

	/* step: if the base directory does not exists, we try and create it */
	if r.fs.IsDirectory(*mount_point) == false {
		glog.Infof("Creating the base directory: %s for you", *mount_point)
		if err := r.fs.Mkdirp(*mount_point); err != nil {
			glog.Errorf("Failed to create the base directory: %s, error: %s", *mount_point, err)
			return err
		}
	}

	/* step: perform a one-time build of the configuration store */
	if *pre_synchronize {
		glog.Infof("Starting the sychronization between mount: %s and store: %s", *mount_point, r.kv.URL())
		if err := r.BuildFileSystem(); err != nil {
			glog.Errorf("Failed to build the initial filesystem, error: %s", err)
			return err
		}
	}

	/*
		Jump into the event loop; we wait for

		- a change to occur in the K/V store
		- a timer event to occur and enforce a refresh of the config
		- a notification of file changes on the config directory
		- a template resource has changed and we need to update the config store
		- a shutdown signal to occur

	*/
	go func() {
		/* step: add a watch on the K/V store for the root directory - i.e. watch for ALL changes */
		if _, err := r.kv.Watch("/", r.nodeEventChannel); err != nil {
			glog.Errorf("Failed to add watch to root directory, error: %s", err)
			return
		}

		/* step: enter into the main event loop */
		for {
			select {
			case event := <-r.nodeEventChannel:
				/* change to the k/v */
				go r.HandleNodeEvent(event)
			case event := <-r.dynamicEventChannel:
				/* a template has changed */
				go r.HandleTemplateEvent(event)
			case event := <-r.filesystemEventChannel:
				/* the file system in the configuration directory has changed */
				go r.HandleFileNotificationEvent(event)
			case <-r.timerEventChannel.C:
				/* a timer has kicked off */
				go r.HandleTimerEvent()
			case <-r.shutdownChannel:
				/* we have recieved a request to shutdown */
				glog.Infof("Recieved the shutdown signal ... shutting down now")
				break
			}
		}
		/* step: if requested, delete the configuration directory */
		if *delete_on_exit {
			r.DeleteConfiguration()
		}
	}()
	return nil
}

/* we delete all the configuration files */
func (r *ConfigurationStore) DeleteConfiguration() error {
	glog.Infof("Deleting the entire configuration directory: %s as requested", *mount_point)
	if err := r.fs.Rmdir(*mount_point); err != nil {
		glog.Errorf("Failed to removing the configuration directory: %s, error: %s", *mount_point, err)
		return err
	}
	return nil
}

/* ============== EVENT HANDLING ================= */
func (r *ConfigurationStore) HandleFileNotificationEvent(event *fsnotify.Event) {
	glog.V(VERBOSE_LEVEL).Infof("HandleFileNotificationEvent() event: %s", event)

}

/* Handle a change to the templated resource */
func (r *ConfigurationStore) HandleTemplateEvent(path string) {
	glog.V(VERBOSE_LEVEL).Infof("HandleTemplateEvent() recieved node event: %s, resynchronizing", path)
	if resource, found := r.dynamic.IsDynamic(path); !found {
		glog.Errorf("The resource for path: %s no longer exists, internal error", path)
		return
	} else {
		/* step: we get the content of the template */
		if content, err := resource.Content(false); err != nil {
			glog.Errorf("Failed to generate the content from template: %s, error: %s", path, err)
			return
		} else {
			/* step: get the file system path */
			full_path := r.FullPath(path)
			/* step: update the content of the file */
			glog.V(VERBOSE_LEVEL).Infof("Updating the content for template: %s", path)
			if err := r.fs.Create(full_path, content); err != nil {
				glog.Errorf("Failed to update the template: %s, error: %s", full_path, err)
				return
			}
		}
	}
}

/* We have a timer event, let force re-sync the configuration */
func (r *ConfigurationStore) HandleTimerEvent() {
	glog.V(VERBOSE_LEVEL).Infof("HandleTimerEvent() recieved ticker event , kicking off a synchronization")
}

/* Handle changes to the K/V store and reflect in the directory */
func (r *ConfigurationStore) HandleNodeEvent(event kv.NodeChange) {
	glog.V(VERBOSE_LEVEL).Infof("HandleNodeEvent() recieved node event: %s, synchronizing", event)
	node := event.Node
	/* check: an update or deletion */
	switch event.Operation {
	case kv.DELETED:
		if node.IsDir() {
			r.DeleteStoreConfigDirectory(node.Path)
		} else {
			r.DeleteStoreConfigFile(node.Path)
		}
	case kv.CHANGED:
		if node.IsDir() {
			r.UpdateStoreConfigDirectory(node.Path)
		} else {
			r.UpdateStoreConfigFile(node.Path, node.Value)
		}
	default:
		glog.Errorf("HandleNodeEvent() unknown operation, skipping the event: %s", event)
	}
}

/* ====================== Store K/V handling =========================== */

/* Delete a file from the config store */
func (r *ConfigurationStore) DeleteStoreConfigFile(path string) error {
	/* the actual file system path */
	full_path := r.FullPath(path)
	glog.V(VERBOSE_LEVEL).Infof("DeleteStoreConfigFile() Deleting configuration file: %s from the store", full_path)
	/* step: check it exists and is a file */
	if !r.fs.Exists(full_path) || !r.fs.IsFile(full_path) {
		glog.Errorf("Failed to delete file: %s, either it doesnt exists or is not a file", full_path)
		return errors.New("Failed to delete, either it doesnt exists or is not a file")
	}
	/* check: is the file a templated resource */
	if _, found := r.dynamic.IsDynamic(path); found {
		glog.V(VERBOSE_LEVEL).Infof("Deleting the templated resource: %s", full_path)
		/* step: free up the resources from the resource manager */
		r.dynamic.Delete(path)
	}
	/* step: delete the file */
	if err := r.fs.Delete(full_path); err != nil {
		glog.Errorf("Failed to delete the file: %s, error: %s", full_path, err)
		return err
	}
	return nil
}

func (r *ConfigurationStore) DeleteStoreConfigDirectory(path string) error {
	/* the actual file system path */
	full_path := r.FullPath(path)
	glog.V(3).Infof("Deleting configuration directory: %s from the store", full_path)
	/* step: check it is a actual directory */
	if _, err := r.CheckDirectory(full_path); err != nil {
		glog.Errorf("Failed to remove the directory: %s, error: %s", full_path, err)
		return err
	}

	/* @TODO step: we need to remove any templated resources which were in the directory */

	/* step: delete the directory and all the children */
	if err := r.fs.Rmdir(full_path); err != nil {
		glog.Errorf("Failed to delete the directory: %s, error: %s", full_path, err)
		return err
	}
	return nil
}

/* create a new directory in the configuration store */
func (r *ConfigurationStore) UpdateStoreConfigDirectory(path string) error {
	/* the actual file system path */
	full_path := r.FullPath(path)
	glog.V(3).Infof("Creating config directory: %s", full_path)

	/* step: we need to make sure the directory structure exists */
	if err := r.fs.Mkdirp(full_path); err != nil {
		glog.Errorf("Failed to ensure the directory: %s, error: %s", full_path, err)
		return err
	}
	/* @TODO step: we add the new directory to the watch list */

	return nil
}

/* create or update a file in the configuration store */
func (r *ConfigurationStore) UpdateStoreConfigFile(path string, value string) error {
	/* the actual file system path */
	full_path := r.FullPath(path)

	/* step: we need to ensure the directory structure exists */
	if err := r.fs.Mkdirp(r.fs.Dirname(full_path)); err != nil {
		glog.Errorf("Failed to ensure the directory: %s, error: %s", r.fs.Dirname(full_path), err)
		return err
	}

	/*
		if this is true a templated resource already exists and the template content has been changed - thus we need to
		update the content of the template
		 - delete the old templated resource
		 - create a new templated resource
	*/

	if _, found := r.dynamic.IsDynamic(path); found {
		glog.V(3).Infof("Dyanmic resource: %s has changes, updating now", path)
		/* step: delete the resource */
		r.dynamic.Delete(path)
		/* step: recreate the template */
		if content, err := r.dynamic.Create(path, value, r.dynamicEventChannel); err != nil {
			glog.Errorf("Failed to update the template for path: %s, error: %s", path, err)
			return err
		} else {
			glog.V(3).Infof("Updated the template for resource: %s", path)
			if err := r.fs.Create(full_path, content); err != nil {
				glog.Errorf("Failed to create the file: %s, error: %s", full_path, err)
				return err
			}
		}
		/* - A node has changed, its value has a templated resource prefix and hasn't already been created i.e. its a new template */
	} else if r.dynamic.IsDynamicContent(path, value) {
		glog.V(3).Infof("Creating a new dynamic resource templated resource: %s", path)
		if content, err := r.dynamic.Create(path, value, r.dynamicEventChannel); err != nil {
			glog.Errorf("Failed to create the template for path: %s, error: %s", path, err)
			return err
		} else {
			if err := r.fs.Create(full_path, content); err != nil {
				glog.Errorf("Failed to create the file: %s, error: %s", full_path, err)
				return err
			}
		}
	} else {
		glog.V(3).Infof("Creating a new config file: %s", path)
		/* step: create a normal file from the content */
		if err := r.fs.Create(full_path, value); err != nil {
			glog.Errorf("Failed to create the file: %s, error: %s", full_path, err)
			return err
		}
	}
	return nil
}

/* Converts the k/v path to the full path on disk - essentially mount_point + node_path */
func (r *ConfigurationStore) FullPath(path string) string {
	return fmt.Sprintf("%s%s", *mount_point, path)
}

func (r *ConfigurationStore) CheckDirectory(path string) (bool, error) {
	if r.fs.Exists(path) == false {
		return false, fs.DirectoryDoesNotExistErr
	}
	if r.fs.IsDirectory(path) == false {
		return false, fs.IsNotDirectoryErr
	}
	return true, nil
}

func (r *ConfigurationStore) BuildFileSystem() error {
	glog.Infof("Building the file system from k/v stote at: %s", *mount_point)
	r.BuildDirectory("/")
	return nil
}

func (r *ConfigurationStore) BuildDirectory(directory string) error {
	/* step: we get a listing of the files under the directory */
	listing, err := r.kv.List(directory)
	if err != nil {
		glog.Errorf("Failed to get listing from directory: %s, error: %s", directory, err)
		return err
	} else {
		glog.V(VERBOSE_LEVEL).Infof("BuildDiectory() processing directory: %s", directory)
		for _, node := range listing {
			full_path := r.FullPath(node.Path)
			glog.V(5).Infof("BuildDirectory() directory: %s, full path: %s", directory, full_path)
			switch {
			case node.IsFile():
				content := node.Value
				/* step: if the file does not exist, create it */
				glog.V(VERBOSE_LEVEL).Infof("BuildDirectory() Creating the file: %s", full_path)
				/* step: check if the content is templated */
				if r.dynamic.IsDynamicContent(node.Path, node.Value) {
					content, err = r.dynamic.Create(node.Path, node.Value, r.dynamicEventChannel)
					if err != nil {
						glog.Errorf("Failed to create the templated file: %s, error: %s", full_path, err)
						continue
					}
				}
				if err := r.fs.Create(full_path, content); err != nil {
					glog.Errorf("Failed to create the file: %s, error: %s", full_path, err)
				}
			case node.IsDir():
				if r.fs.Exists(full_path) == false {
					glog.V(VERBOSE_LEVEL).Infof("BuildDiectory() creating directory item: %s", full_path)
					r.fs.Mkdir(full_path)
				}
				/* go recursive and build the contents of that directory */
				if err := r.BuildDirectory(node.Path); err != nil {
					glog.Errorf("Failed to build the item directory: %s, error: %s", full_path, err)
				}
			}
		}
	}
	return nil
}
