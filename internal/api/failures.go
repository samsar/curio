package api

import (
	"context"
	"net/http"
)

// failureTopHosts is how many hosts GET /v1/failures names for each cause:
// the few that account for most of it, the sites a refetch by cause and
// host would go after first.
const failureTopHosts = 5

// FailuresResponse is the body of GET /v1/failures: the tenant's failed
// and dead documents, counted by cause. Its slices are never nil, so they
// never go out as null.
type FailuresResponse struct {
	Total  int                 `json:"total"`
	Causes []FailureCauseCount `json:"causes"`
}

// FailureCauseCount is how many documents failed for one cause, and the
// hosts most of them are on.
type FailureCauseCount struct {
	Cause string      `json:"cause"`
	Count int         `json:"count"`
	Hosts []HostCount `json:"hosts"`
}

// HostCount is how many of a cause's documents are on one host.
type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

func (d Deps) handleFailures(w http.ResponseWriter, r *http.Request) {
	resp, err := d.failures(r.Context())
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// failures counts the tenant's failed and dead documents by cause, the
// most first, each with the failureTopHosts hosts most of its documents
// are on. A host is as GET /v1/documents?host= matches it, so a cause and
// host pair lists exactly the documents it counts.
func (d Deps) failures(ctx context.Context) (FailuresResponse, error) {
	summary, err := d.Documents.FailureSummary(ctx, d.TenantID, failureTopHosts)
	if err != nil {
		return FailuresResponse{}, err
	}
	out := FailuresResponse{Total: summary.Total, Causes: make([]FailureCauseCount, 0, len(summary.Causes))}
	for _, c := range summary.Causes {
		hosts := make([]HostCount, 0, len(c.Hosts))
		for _, h := range c.Hosts {
			hosts = append(hosts, HostCount{Host: h.Host, Count: h.Count})
		}
		out.Causes = append(out.Causes, FailureCauseCount{Cause: string(c.Cause), Count: c.Count, Hosts: hosts})
	}
	return out, nil
}
