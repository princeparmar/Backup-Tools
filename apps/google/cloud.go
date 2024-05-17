package google

import (
	"context"
	"fmt"

	rm "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"github.com/labstack/echo/v4"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

func ListProjects(c echo.Context) (any, error) {
	client, err := client(c)
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}

	pc, err := rm.NewProjectsRESTClient(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create projects REST client: %w", err)
	}
	defer pc.Close()

	rqst := &resourcemanagerpb.ListProjectsRequest{
		Parent: "organizations/0", // Ensure the correct organization ID is used
	}

	it := pc.ListProjects(context.Background(), rqst)

	var projects []any
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list projects: %w", err)
		}
		projects = append(projects, resp)
	}

	return projects, nil
}
