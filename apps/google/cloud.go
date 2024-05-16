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
		return nil, err
	}
	pc, err := rm.NewProjectsClient(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, nil
	}
	defer pc.Close()
	//it := pc.ListProjects(context.Background(), &resourcemanagerpb.ListProjectsRequest{})
	rqst := &resourcemanagerpb.SearchProjectsRequest{
		Query: fmt.Sprintf("name:*"),
	}
	it := pc.SearchProjects(context.Background(), rqst)
	var p []any
	for {
		resp, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		// TODO: Use resp.
		p = append(p, resp)

		// If you need to access the underlying RPC response,
		// you can do so by casting the `Response` as below.
		// Otherwise, remove this line. Only populated after
		// first call to Next(). Not safe for concurrent access.
		_ = it.Response.(*resourcemanagerpb.ListProjectsResponse)
	}

	return p, nil
}
