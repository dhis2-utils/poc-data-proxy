package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// broker drives d2-broker (DHIS2_BROKER_URL, DHIS2_BROKER_TOKEN): disposable DHIS2
// instances for live test runs.
type broker struct {
	url, token string
}

func newBroker() (*broker, error) {
	b := &broker{os.Getenv("DHIS2_BROKER_URL"), os.Getenv("DHIS2_BROKER_TOKEN")}
	if b.url == "" || b.token == "" {
		return nil, fmt.Errorf("DHIS2_BROKER_URL and DHIS2_BROKER_TOKEN must be set")
	}
	return b, nil
}

func (b *broker) do(method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		json.Unmarshal(data, out)
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %d: %.500s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode, nil
}

type instance struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	DevnetURL string `json:"devnet_url"`
}

func (b *broker) instance(name string) (*instance, error) {
	var res struct {
		Instances []instance `json:"instances"`
	}
	if _, err := b.do("GET", "/instances", nil, &res); err != nil {
		return nil, err
	}
	for _, i := range res.Instances {
		if i.Name == name {
			return &i, nil
		}
	}
	return nil, nil
}

// wait polls a job until it ends.
func (b *broker) wait(job string) error {
	for {
		var j struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if _, err := b.do("GET", "/jobs/"+job, nil, &j); err != nil {
			return err
		}
		switch j.Status {
		case "succeeded":
			return nil
		case "failed", "cancelled":
			return fmt.Errorf("job %s %s: %s (see %s/jobs/%s/log)", job, j.Status, j.Error, b.url, job)
		}
		time.Sleep(10 * time.Second)
	}
}

// fresh returns the dev-net URL of instance name in a clean state: created with
// version and seed, or reset to seed, or (no seed) deleted and created empty.
func (b *broker) fresh(name, version, seed, label string) (string, error) {
	cur, err := b.instance(name)
	if err != nil {
		return "", err
	}
	var job struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if cur != nil && seed != "" {
		if cur.Status != "running" {
			if _, err := b.do("POST", "/instances/"+name+"/start", nil, &job); err != nil {
				return "", err
			}
			if err := b.wait(job.Job.ID); err != nil {
				return "", err
			}
		}
		log.Printf("resetting %s to %s", name, seed)
		if _, err := b.do("POST", "/instances/"+name+"/reset", map[string]any{"seed": seed}, &job); err != nil {
			return "", err
		}
	} else {
		if cur != nil {
			log.Printf("deleting %s for a clean empty instance", name)
			if _, err := b.do("DELETE", "/instances/"+name, nil, &job); err != nil {
				return "", err
			}
			if err := b.wait(job.Job.ID); err != nil {
				return "", err
			}
		}
		body := map[string]any{"name": name, "version": version, "label": label, "memory": "4g"}
		if seed != "" {
			body["seed"] = seed
		}
		log.Printf("creating %s (DHIS2 %s, seed %q)", name, version, seed)
		if _, err := b.do("POST", "/instances", body, &job); err != nil {
			return "", err
		}
	}
	if err := b.wait(job.Job.ID); err != nil {
		return "", err
	}
	cur, err = b.instance(name)
	if err != nil || cur == nil {
		return "", fmt.Errorf("instance %s not found after job: %v", name, err)
	}
	return cur.DevnetURL, nil
}

func (b *broker) stop(name string) error {
	var job struct {
		Job struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if _, err := b.do("POST", "/instances/"+name+"/stop", nil, &job); err != nil {
		return err
	}
	return b.wait(job.Job.ID)
}
