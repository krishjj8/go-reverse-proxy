package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

type CloudWatchMetrics struct {
	client    *cloudwatch.Client
	namespace string
}

func NewCloudWatchMetrics(namespace string) *CloudWatchMetrics {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		slog.Warn("AWS SDK default configuration load failed. Falling back to local logging mode.", "error", err)
		return &CloudWatchMetrics{client: nil, namespace: namespace}
	}

	return &CloudWatchMetrics{
		client:    cloudwatch.NewFromConfig(cfg),
		namespace: namespace,
	}
}

func (m *CloudWatchMetrics) RecordLatency(upstream string, latencyMs float64) {
	if m.client == nil {
		return
	}

	_, err := m.client.PutMetricData(context.TODO(), &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(m.namespace),
		MetricData: []types.MetricDatum{
			{
				MetricName: aws.String("ProxyLatency"),
				Value:      aws.Float64(latencyMs),
				Unit:       types.StandardUnitMilliseconds,
				Timestamp:  aws.Time(time.Now()),
				Dimensions: []types.Dimension{
					{
						Name:  aws.String("Upstream"),
						Value: aws.String(upstream),
					},
				},
			},
		},
	})
	if err != nil {
		slog.Error("Failed to stream metric point to AWS CloudWatch API", "error", err)
	}
}
