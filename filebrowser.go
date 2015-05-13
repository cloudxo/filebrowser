package main

import (
	"flag"
	"fmt"
	"html/template"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	humanize "github.com/dustin/go-humanize"
	"github.com/gorilla/mux"
	"golang.org/x/net/context"
	"golang.org/x/oauth2/google"
	storage "google.golang.org/api/storage/v1"
	cloud "google.golang.org/cloud/storage"
)

const (
	bucketName = "bucket.gmbuell.com"
	projectID  = "gmbuell-cloud"

	scope      = storage.DevstorageFull_controlScope
	entityName = "allUsers"
)

var (
	jsonFile       = flag.String("creds", "key.json", "A path to your JSON key file for your service account downloaded from Google Developer Console, not needed if you run it on Compute Engine instances.")
	host           = flag.String("host", "0.0.0.0", "IP of host to run webserver on")
	port           = flag.Int("port", 8080, "Port to run webserver on")
	googleAccessId = flag.String("googleAccessId", "115985846185-gmc25e88t3ochacb6hednp2obujn0c5k@developer.gserviceaccount.com", "Google service account client email address xx@developer.gserviceaccount.com")
	pemFilename    = flag.String("pemFilename", "key.pem", "Google Service Account PEM file.")
)

func fatalf(service *storage.Service, errorMessage string, args ...interface{}) {
	log.Fatalf("Dying with error:\n"+errorMessage, args...)
}

type Server struct {
	StorageService       *storage.Service
	Templates            *template.Template
	StorageAccessOptions *cloud.SignedURLOptions
}

type ByUpdated []*storage.Object

func (a ByUpdated) Len() int           { return len(a) }
func (a ByUpdated) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByUpdated) Less(i, j int) bool { return a[i].Updated > a[j].Updated }

func (s *Server) SignUrl(objectName string) string {
	s.StorageAccessOptions.Expires = time.Now().Add(time.Second * 60 * 60 * 6) //expire in 6 hours
	escapedName := url.QueryEscape(objectName)
	escapedName = strings.Replace(escapedName, "+", "%20", -1)
	getURL, err := cloud.SignedURL(bucketName, escapedName, s.StorageAccessOptions)
	if err == nil {
		return getURL
	} else {
		log.WithFields(log.Fields{
			"objectName":    objectName,
			"internalError": err,
		}).Warn("Error signing URL.")
		return ""
	}
}

func FilterVideos(objectList []*storage.Object) []*storage.Object {
	var videoObjects = make([]*storage.Object, 0, len(objectList))
	for _, object := range objectList {
		if strings.HasSuffix(object.Name, ".mp4") {
			videoObjects = append(videoObjects, object)
		}
	}
	return videoObjects
}

func CleanupName(objectName string) string {
	return strings.TrimSuffix(objectName, ".mp4")
}

func (s *Server) RootHandler(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-type", "text/html")

	// List all objects in a bucket
	res, err := s.StorageService.Objects.List(bucketName).Do()
	if err != nil {
		log.WithFields(log.Fields{
			"internalError": err,
		}).Warn("Failed getting video list.")
	}

	sort.Sort(ByUpdated(res.Items))

	s.Templates.ExecuteTemplate(response, "index.html", res)
}

func (s *Server) PlayHandler(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-type", "text/html")
	vars := mux.Vars(request)
	objectName := vars["objectName"]

	// List all objects in a bucket
	res, err := s.StorageService.Objects.Get(bucketName, objectName).Do()
	if err != nil {
		log.WithFields(log.Fields{
			"objectName":    objectName,
			"internalError": err,
		}).Warn("Failed getting info for video.")
	}

	s.Templates.ExecuteTemplate(response, "play.html", res)
}

func main() {
	flag.Parse()

	if *jsonFile != "" {
		os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", *jsonFile)
	}
	client, err := google.DefaultClient(context.Background(), scope)
	if err != nil {
		log.Fatalf("Unable to get default client: %v", err)
	}

	service, err := storage.New(client)
	if err != nil {
		log.Fatalf("Unable to create storage service: %v", err)
	}

	// Settings for signed url
	pemFile, err := ioutil.ReadFile(*pemFilename)
	if err != nil {
		log.WithFields(log.Fields{
			"PEM File": pemFilename,
		}).Fatal(err)
	}

	server := new(Server)
	server.StorageService = service

	humanTime := func(inputTime string) string {
		parsedTime, err := time.Parse(time.RFC3339Nano, inputTime)
		if err != nil {
			log.WithFields(log.Fields{
				"inputTime":     inputTime,
				"internalError": err,
			}).Warn("Could not parse timestamp.")
			return humanize.Time(time.Now())
		}
		return humanize.Time(parsedTime)
	}

	server.Templates = template.Must(template.New("main").Funcs(template.FuncMap{
		"humanSize":    humanize.Bytes,
		"humanTime":    humanTime,
		"sign":         server.SignUrl,
		"filterVideos": FilterVideos,
		"cleanupName":  CleanupName,
	}).ParseGlob("templates/*.html"))
	server.StorageAccessOptions = &cloud.SignedURLOptions{
		GoogleAccessID: *googleAccessId,
		PrivateKey:     pemFile,
		Method:         "GET",
	}

	r := mux.NewRouter().StrictSlash(false)
	r.HandleFunc("/", server.RootHandler)
	r.HandleFunc("/play/{objectName}", server.PlayHandler)

	addr := fmt.Sprintf("%s:%d", *host, *port)
	log.WithFields(
		log.Fields{
			"host": *host,
			"port": *port,
		}).Info("Starting webserver.")
	log.Fatal(http.ListenAndServe(addr, r))
}
