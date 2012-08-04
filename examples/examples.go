// Package examples implements the DB and S3 backed examples backend for Rell.
package examples

import (
	"crypto/md5"
	"errors"
	"flag"
	"fmt"
	"github.com/daaku/go.errcode"
	"github.com/daaku/go.flag.pkgpath"
	"github.com/daaku/rell/redis"
	"github.com/simonz05/godis"
	"io/ioutil"
	"launchpad.net/goamz/aws"
	"launchpad.net/goamz/s3"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Some categories are hidden from the listing.
var hidden = map[string]bool{
	"bugs":   true,
	"fbml":   true,
	"hidden": true,
	"secret": true,
	"tests":  true,
}

type Example struct {
	Name    string `json:"-"`
	Content []byte `json:"-"`
	AutoRun bool   `json:"autoRun"`
	Title   string `json:"-"`
	URL     string `json:"-"`
}

type Category struct {
	Name    string
	Example []*Example
	Hidden  bool
}

type DB struct {
	Category []*Category
}

var (
	// Directory for disk backed DBs.
	oldExamplesDir string
	newExamplesDir string

	// We have two disk backed DBs.
	old *DB
	mu  *DB

	// For stored examples.
	bucketMemo *s3.Bucket
	bucketName string
	auth       aws.Auth

	// Stock response for the index page.
	emptyExample = &Example{Title: "Welcome", URL: "/"}
)

// We load up the examples into memory on server start.
func init() {
	flag.StringVar(
		&bucketName,
		"rell.amazon.bucket",
		"fbrell_examples",
		"The Amazon bucket to store examples.")
	flag.StringVar(
		&auth.AccessKey,
		"rell.amazon.key",
		"",
		"The Amazon API key to access the bucket.")
	flag.StringVar(
		&auth.SecretKey,
		"rell.amazon.secret",
		"",
		"The Amazon API secret to access the bucket.")
	pkgpath.DirVar(
		&oldExamplesDir,
		"rell.examples.old",
		"github.com/daaku/rell/examples/db/old",
		"The directory containing examples for the old SDK.")
	pkgpath.DirVar(
		&newExamplesDir,
		"rell.examples.new",
		"github.com/daaku/rell/examples/db/mu",
		"The directory containing examples for the new SDK.")
}

// Get's the shared S3 bucket instance.
func Bucket() *s3.Bucket {
	if bucketMemo == nil {
		bucketMemo = s3.New(auth, aws.USEast).Bucket(bucketName)
	}
	return bucketMemo
}

// Loads a specific examples directory.
func loadDir(name string) (*DB, error) {
	categories, err := ioutil.ReadDir(name)
	if err != nil {
		return nil, fmt.Errorf("Failed to read directory %s: %s", name, err)
	}
	db := &DB{Category: make([]*Category, 0, len(categories))}
	for _, categoryFileInfo := range categories {
		categoryName := categoryFileInfo.Name()
		if !categoryFileInfo.IsDir() {
			log.Printf(
				"Got unexpected file instead of directory for category: %s",
				categoryName)
			continue
		}
		category := &Category{
			Name:   categoryName,
			Hidden: hidden[categoryName],
		}
		categoryDir := filepath.Join(name, categoryName)
		examples, err := ioutil.ReadDir(categoryDir)
		if err != nil {
			return nil, fmt.Errorf("Failed to read category %s: %s", categoryDir, err)
		}
		category.Example = make([]*Example, 0, len(examples))
		for _, example := range examples {
			exampleName := example.Name()
			exampleFile := filepath.Join(categoryDir, exampleName)
			content, err := ioutil.ReadFile(exampleFile)
			if err != nil {
				return nil, fmt.Errorf(
					"Failed to read example %s: %s", exampleFile, err)
			}
			cleanName := exampleName[:len(exampleName)-5]
			category.Example = append(category.Example, &Example{
				Name:    cleanName,
				Content: content,
				AutoRun: true,
				Title:   categoryName + " · " + cleanName,
				URL:     path.Join("/", categoryName, cleanName),
			})
		}
		db.Category = append(db.Category, category)
	}
	return db, nil
}

// Load an Example for a given version and path.
func Load(version, path string) (*Example, error) {
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[1] == "" {
		return emptyExample, nil
	} else if len(parts) == 4 {
		if parts[1] != "raw" && parts[1] != "simple" {
			return nil, errcode.New(http.StatusNotFound, "Invalid URL: %s", path)
		}
		parts = []string{"", parts[2], parts[3]}
	} else if len(parts) != 3 {
		return nil, errcode.New(http.StatusNotFound, "Invalid URL: %s", path)
	}

	if parts[1] == "saved" {
		content, err := cachedGet(parts[2])
		if err != nil {
			return nil, err
		}
		return &Example{
			Content: content,
			Title:   "Stored Example",
			URL:     path,
		}, nil
	}
	category := GetDB(version).FindCategory(parts[1])
	if category == nil {
		return nil, errcode.New(http.StatusNotFound, "Could not find category: %s", parts[1])
	}
	example := category.FindExample(parts[2])
	if example == nil {
		return nil, errcode.New(http.StatusNotFound, "Could not find example: %s", parts[2])
	}
	return example, nil
}

// Get the DB for a given SDK Version.
func GetDB(version string) *DB {
	var err error
	if version == "mu" {
		if mu == nil {
			mu, err = loadDir(newExamplesDir)
			if err != nil {
				log.Fatal(err)
			}
		}
		return mu
	}
	if old == nil {
		old, err = loadDir(oldExamplesDir)
		if err != nil {
			log.Fatal(err)
		}
	}
	return old
}

// Find a category by it's name.
func (d *DB) FindCategory(name string) *Category {
	for _, category := range d.Category {
		if category.Name == name {
			return category
		}
	}
	return nil
}

// Find an example by it's name.
func (c *Category) FindExample(name string) *Example {
	for _, example := range c.Example {
		if example.Name == name {
			return example
		}
	}
	return nil
}

// Save an Example.
func Save(content []byte) (string, error) {
	if len(content) > 10240 {
		return "", errcode.New(
			http.StatusRequestEntityTooLarge,
			"Maximum allowed size is 10 kilobytes.")
	}
	h := md5.New()
	_, err := h.Write(content)
	if err != nil {
		return "", fmt.Errorf("Error comupting md5 sum: %s", err)
	}
	key := fmt.Sprintf("%x", h.Sum(nil))
	item, err := cachedGet(key)
	if item == nil {
		// we don't have this stored
		err = Bucket().Put(key, content, "text/plain", s3.Private)
		if err != nil {
			log.Printf("Error in bucket.Put: %s", err)
		}
		err = redis.Client().Set(makeCacheKey(key), content)
		if err != nil {
			log.Printf("Error in cache.Set: %s", err)
		}
	}
	return key, err
}

func makeCacheKey(key string) string {
	return bucketName + ":" + key
}

var errIgnore = errors.New("")

func cacheOnlyGet(cacheKey string) ([]byte, error) {
	item, err := redis.Client().Get(cacheKey)
	if err != nil {
		if err == redis.ErrKeyNotFound {
			return nil, errIgnore
		}
		return nil, err
	}
	return item.Bytes(), nil
}

func s3OnlyGet(key, cacheKey string) ([]byte, error) {
	content, err := Bucket().Get("/" + key)
	if err != nil {
		s3Err, ok := err.(*s3.Error)
		if ok && s3Err.StatusCode == http.StatusNotFound {
			return nil, errcode.New(
				http.StatusNotFound, "Could not find saved example.")
		}
		log.Printf("Unknown S3 error: %s", err)
		return nil, err
	}
	go func() {
		err := redis.Client().Set(cacheKey, content)
		if err != nil {
			log.Printf("Error in cache.Set: %s", err)
		}
	}()
	return content, nil
}

// Check cache and then S3 for a stored example.
func cachedGet(key string) ([]byte, error) {
	cacheKey := makeCacheKey(key)
	type content struct {
		Data  []byte
		Error error
	}
	response := make(chan content, 2)
	go func() {
		data, err := cacheOnlyGet(cacheKey)
		response <- content{data, err}
	}()
	go func() {
		data, err := s3OnlyGet(key, cacheKey)
		response <- content{data, err}
	}()
	for {
		select {
		case r := <-response:
			if r.Error == godis.ErrKeyNotFound {
				continue
			}
			return r.Data, r.Error
		case <-time.After(30 * time.Second):
			return nil, errcode.New(
				http.StatusRequestTimeout, "Failed to retrieve example.")
		}
	}
	panic("Not reached")
}
