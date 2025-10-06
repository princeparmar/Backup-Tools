package google

import (
	"context"
	"fmt"
	"time"

	rm "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/labstack/echo/v4"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
)

func ListProjects(c echo.Context) (any, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("cloud_list_projects", "success", time.Since(start), "service", "cloud")
	}()

	client, err := client(c)
	if err != nil {
		prometheus.RecordError("cloud_auth_error", "get_client", "service", "cloud")
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	pc, err := rm.NewProjectsRESTClient(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		prometheus.RecordError("cloud_service_error", "create_projects_client", "service", "cloud")
		return nil, fmt.Errorf("failed to create projects REST client: %w", err)
	}
	defer pc.Close()

	rqst := &resourcemanagerpb.SearchProjectsRequest{
		//Parent: "organizations/0", // Ensure the correct organization ID is used
	}

	it := pc.SearchProjects(context.Background(), rqst)

	var projects []any
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			prometheus.RecordError("cloud_api_error", "list_projects", "service", "cloud")
			return nil, fmt.Errorf("failed to list projects: %w", err)
		}
		projects = append(projects, resp)
	}

	prometheus.RecordCounter("cloud_projects_retrieved", int64(len(projects)), "service", "cloud")
	return projects, nil
}

func ListOrganizations(c echo.Context) (any, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("cloud_list_organizations", "success", time.Since(start), "service", "cloud")
	}()

	client, err := client(c)
	if err != nil {
		prometheus.RecordError("cloud_auth_error", "get_client", "service", "cloud")
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	pc, err := rm.NewOrganizationsRESTClient(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		prometheus.RecordError("cloud_service_error", "create_organizations_client", "service", "cloud")
		return nil, fmt.Errorf("failed to create projects REST client: %w", err)
	}
	defer pc.Close()

	rqst := &resourcemanagerpb.SearchOrganizationsRequest{
		//Parent: "organizations/0", // Ensure the correct organization ID is used
	}

	it := pc.SearchOrganizations(context.Background(), rqst)

	orgs := []any{}
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			prometheus.RecordError("cloud_api_error", "list_organizations", "service", "cloud")
			return nil, fmt.Errorf("failed to list projects: %w", err)
		}
		orgs = append(orgs, resp)
	}

	prometheus.RecordCounter("cloud_organizations_retrieved", int64(len(orgs)), "service", "cloud")
	return orgs, nil
}
