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

package kv

import (
	"flag"
	"errors"
	"net/url"

	"github.com/golang/glog"
)

const (
	DEFAULT_KV_STORE = "etcd://localhost:4001"
	VERBOSE_LEVEL    = 6
)

var (
	kv_store_url *string
	InvalidUrlErr = errors.New("Invalid URI error, please check backend url")
	InvalidDirectoryErr = errors.New("Invalid directory specified")
)

func init() {
	kv_store_url = flag.String("store", DEFAULT_KV_STORE, "the url for key / value store")
}


type KVStore interface {
	/* get the url for the kv store */
	URL() string
	/* retrieve a key from the store */
	Get(key string) (*Node, error)
	/* Get a list of all the nodes under the path */
	List(path string) ([]*Node, error)
	/* set a key in the store */
	Set(key string, value string) error
	/* delete a key from the store */
	Delete(key string) error
	/* recursively delete a path */
	RemovePath(path string) error
	/* Create a directory node */
	Mkdir(path string) error
	/* watch for changes on the key */
	Watch(key string, updateChannel NodeUpdateChannel) (chan bool, error)
}

func NewKVStore() (KVStore, error) {
	glog.Infof("Creating a new configuration provider: %s", *kv_store_url)
	/* step: parse the url */
	if uri, err := url.Parse(*kv_store_url); err != nil {
		glog.Errorf("Failed to parse the url: %s, error: %s", *kv_store_url, err)
		return nil, err
	} else {
		switch uri.Scheme {
		case "etcd":
			agent, err := NewEtcdStoreClient(uri)
			if err != nil {
				glog.Errorf("Failed to create the K/V agent, error: %s", err)
				return nil, err
			}
			return agent, nil
		default:
			return nil, errors.New("Unsupported key/value store: " + *kv_store_url)
		}
	}
}
