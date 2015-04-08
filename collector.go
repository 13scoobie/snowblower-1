package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/awslabs/aws-sdk-go/aws"
	"github.com/awslabs/aws-sdk-go/service/sns"

	"code.google.com/p/go-uuid/uuid"
)

type collector struct {
	publisher Publisher
}

func (c *collector) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	var networkID string
	spCookie, err := request.Cookie("sp")
	if err != nil {
		networkID = uuid.NewRandom().String()
	} else {
		networkID = spCookie.Value
	}

	http.SetCookie(w, &http.Cookie{
		Name:    "sp",
		Value:   networkID,
		Expires: time.Now().AddDate(1, 0, 0),
		Domain:  os.Getenv("COOKIE_DOMAIN"),
	})

	// TODO set P3P header

	// TODO handle GET request

	switch request.Method {
	case "POST":
		c.servePost(w, request, networkID)
	default:
		w.WriteHeader(http.StatusForbidden)
		w.Write(nil) // TODO make nice message
	}
}

func (c *collector) servePost(
	w http.ResponseWriter,
	request *http.Request,
	networkID string,
) {
	bodyBytes, err := ioutil.ReadAll(request.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(nil) // TODO make nice message
		return
	}

	// this deserialization is used to check if we have any real data. It’s
	// slightly wasteful, but it’s certainly not a deal breaker right now and
	// the savings we get from not shipping empty events is huge

	trackerPayload := TrackerPayload{}
	if err := json.Unmarshal(bodyBytes, &trackerPayload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write(nil) // TODO make nice message
		return
	}

	if len(trackerPayload.Data) > 0 {
		collectorPayload := CollectorPayload{
			Schema:        CollectorPayloadSchema,
			IPAddress:     realRemoteAddr(request),
			Timestamp:     time.Now().UnixNano() / 1000000,
			Collector:     "Snowblower/0.0.1",
			UserAgent:     request.UserAgent(),
			Body:          string(bodyBytes),
			Headers:       requestHeadersAsArray(request),
			NetworkUserID: networkID,
			//Encoding:      "",
			//RefererURI:    "",
			//Path:          "",
			//QueryString:   "",
			//ContentType:   "",
			//Hostname:      "",
		}
		messageBytes, err := json.Marshal(collectorPayload)
		if err != nil {
			log.Printf("Error marshalling JSON: %s", err)
			return
		}
		message := string(messageBytes)
		c.publisher.publish(message)
	}

	w.WriteHeader(http.StatusOK)
	w.Write(nil)
}

func startCollector() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	var credentials aws.CredentialsProvider
	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		credentials = aws.DefaultCreds()
	} else {
		credentials = aws.IAMCreds()
	}

	snsTopic := os.Getenv("SNS_TOPIC")
	if snsTopic == "" {
		panic("SNS_TOPIC required")
	}

	snsService := sns.New(&aws.Config{
		Credentials: credentials,
		Region:      "eu-west-1",
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	healthHandler := &health{}
	http.Handle("/api/health", healthHandler)
	http.Handle("/health", healthHandler)

	collectorHandler := &collector{
		publisher: &SNSPublisher{
			service: snsService,
			topic:   snsTopic,
		},
	}

	http.Handle("/com.snowplowanalytics.snowplow/tp2", collectorHandler)
	http.Handle("/i", collectorHandler)

	portString := fmt.Sprintf(":%s", port)
	log.Printf("Starting server on %s", portString)
	http.ListenAndServe(portString, nil)
}
