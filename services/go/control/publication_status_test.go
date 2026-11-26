package control

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type publicationRecords struct {
	recordController
	completed bool
	lookups   int
	err       error
}

func (r *publicationRecords) IsCanonical(_ context.Context, account, project string) (bool, error) {
	r.lookups++
	if account != ownerID || project != projectID {
		return false, errors.New("unexpected publication owner")
	}
	return r.completed, r.err
}

type batchPublicationRecords struct {
	publicationRecords
	batches int
}

func (r *batchPublicationRecords) PublicationStates(_ context.Context, account string) (map[string]bool, error) {
	r.batches++
	if account != ownerID {
		return nil, errors.New("unexpected batch owner")
	}
	return map[string]bool{projectID: true}, nil
}

func TestPublicationCompletionStateDoesNotImplyVerification(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "published"}[completed], func(t *testing.T) {
			s, _ := fixture(t)
			mustExec(t, s, "INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('repo',?,'tangled','did:plc:owner','skills','main','atproto')", projectID)
			records := &publicationRecords{completed: completed}
			s.cfg.Records = records
			for _, path := range []string{"/projects/" + projectID, "/projects/" + projectID + "/repo-connection"} {
				response := call(t, s, "GET", path, ownerCookie, "", "")
				expectStatus(t, response, 200)
				if object(t, response)["atproto_publication_completed"] != completed {
					t.Fatal("publication receipt missing", response.Body.String())
				}
			}
			if records.engines != 0 {
				t.Fatal("state read accessed the PDS or publication engine")
			}
			before := records.lookups
			expectStatus(t, call(t, s, "GET", "/projects/"+projectID+"/repo-connection", otherCookie, "", ""), 404)
			if records.lookups != before {
				t.Fatal("foreign owner reached publication state lookup")
			}
		})
	}
}

func TestPublicationStateOmittedWithoutRecordStorage(t *testing.T) {
	s, _ := fixture(t)
	mustExec(t, s, "INSERT INTO repo_connections(id,project_id,provider,repo_owner,repo_name,default_branch,auth_type) VALUES('repo',?,'github','owner','skills','main','oauth')", projectID)
	for _, path := range []string{"/projects/" + projectID, "/projects/" + projectID + "/repo-connection"} {
		response := call(t, s, "GET", path, ownerCookie, "", "")
		expectStatus(t, response, 200)
		if _, exists := object(t, response)["atproto_publication_completed"]; exists {
			t.Fatal("unconfigured publication state was asserted", response.Body.String())
		}
	}
}

func TestProjectListBatchesPrivatePublicationStates(t *testing.T) {
	s, _ := fixture(t)
	mustExec(t, s, "INSERT INTO projects(id,account_id,name,slug,subdomain) VALUES('second',?,'Second','second','second')", ownerID)
	records := &batchPublicationRecords{}
	s.cfg.Records = records
	response := call(t, s, "GET", "/projects", ownerCookie, "", "")
	expectStatus(t, response, 200)
	var projects []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || records.batches != 1 || records.lookups != 0 || records.engines != 0 || records.scopes != 0 {
		t.Fatalf("list did not use one private batch: %+v", records)
	}
	for _, project := range projects {
		if project["atproto_publication_completed"] != (project["id"] == projectID) {
			t.Fatal("incorrect per-project publication state", project)
		}
	}
}

func TestPublicationStateLookupFailureIsNotUnpublished(t *testing.T) {
	s, _ := fixture(t)
	s.cfg.Records = &publicationRecords{err: errors.New("database unavailable")}
	expectStatus(t, call(t, s, "GET", "/projects/"+projectID, ownerCookie, "", ""), 503)
}
