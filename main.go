package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"text/template"
	"time"

	"github.com/aws/aws-lambda-go/cfn"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/eks"
	log "github.com/golang/glog"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientset "k8s.io/client-go/kubernetes"
)

func init() {
	flag.Parse()
	flag.Lookup("logtostderr").Value.Set("true")
}

// Run the Lambda
func main() {
	lambda.Start(cfn.LambdaWrap(handler))
}

// Handle the Lambda event
func handler(ctx context.Context, event cfn.Event) (physicalResourceID string, data map[string]interface{}, err error) {

	event.ResourceProperties["PhysicalResourceID"] = lambdacontext.LogStreamName

	data = map[string]interface{}{}

	if event.RequestType == "Create" {
		if err = createAwsAuthConfigMap(event); err != nil {
			log.Errorf("Unable to create aws-auth ConfigMap - reason: %v", err)
		}
	}

	return
}

// Parse and set the values in the YAML spec
func createConfigMapData(nodeInstanceRoleArn, accountId, adminUser, adminRoleArn string) ([]byte, error) {

	configMapTemplate, err := template.New("configMap").Parse(configMapTemplateStr)
	if err != nil {
		return nil, errors.Wrap(err, "Unable create ConfigMap template")
	}

	varmap := map[string]interface{}{
		"NodeInstanceRoleArn": nodeInstanceRoleArn,
		"EC2PrivateDNSName":   "{{EC2PrivateDNSName}}",
		"AdminUserArn":        fmt.Sprintf("arn:aws:iam::%s:user/%s", accountId, adminUser),
		"AdminUser":           adminUser,
		"AdminRoleArn":        adminRoleArn,
	}

	var b bytes.Buffer
	buffer := bufio.NewWriter(&b)
	if err = configMapTemplate.Execute(buffer, varmap); err != nil {
		return nil, errors.Wrap(err, "Unable add params to spec template")
	}
	buffer.Flush()

	return b.Bytes(), nil
}

// Create and populate the ConfigMap with data by initializing the YAML template
func createConfigMap(
	clusterName,
	nodeInstanceRoleArn,
	accountId,
	adminUser,
	adminRoleArn string) (*v1.ConfigMap, error) {

	spec, err := createConfigMapData(nodeInstanceRoleArn, accountId, adminUser, adminRoleArn)
	if err != nil {
		return nil, errors.Wrap(err, "Unable create ConfigMap data")
	}

	log.Infof("Config map: %s", string(spec))

	cm := &v1.ConfigMap{}
	d := serializer.NewCodecFactory(runtime.NewScheme()).UniversalDeserializer()
	if _, _, err := d.Decode(spec, nil, cm); err != nil {
		return nil, errors.Wrap(err, "Invalid spec yaml")
	}

	return cm, nil
}

// Load the EKS cluster ca
func lookupClusterCa(clusterName string) (string, error) {
	input := &eks.DescribeClusterInput{Name: aws.String(clusterName)}

	if result, err := eks.New(session.New()).DescribeCluster(input); err != nil {
		return "", errors.Wrap(err, "Unable to describe cluster:"+clusterName)
	} else {
		return *result.Cluster.CertificateAuthority.Data, nil
	}
}

// Assume the correct role, create the session and auth with K8S
func initClientset(clusterName, clusterEndpoint, createRoleArn string) (*clientset.Clientset, error) {

	clusterCa, err := lookupClusterCa(clusterName)
	if err != nil {
		return nil, errors.Wrap(err, "Unable to load cluster ca")
	}

	clientset, err := NewAuthClient(
		&ClusterConfig{
			ClusterName:              clusterName,
			MasterEndpoint:           clusterEndpoint,
			CertificateAuthorityData: clusterCa,
			Session:                  session.Must(session.NewSession()),
		},
	)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to create clientset")
	}

	return clientset, nil
}

// Populate and dispatch the ConfigMap create event to the cluster
func createAwsAuthConfigMap(event cfn.Event) error {

	accountId, _ := event.ResourceProperties["AccountId"].(string)
	createRoleArn := event.ResourceProperties["CreateRoleArn"].(string)
	clusterName, _ := event.ResourceProperties["ClusterName"].(string)
	clusterEndpoint, _ := event.ResourceProperties["ClusterEndpoint"].(string)
	adminUser, _ := event.ResourceProperties["AdminUser"].(string)
	adminRoleArn, _ := event.ResourceProperties["AdminRoleArn"].(string)
	nodeInstanceRoleArn, _ := event.ResourceProperties["NodeInstanceRoleArn"].(string)

	clientset, err := initClientset(clusterName, clusterEndpoint, createRoleArn)
	if err != nil {
		return errors.Wrap(err, "clientset init failed")
	}

	if configMap, err := createConfigMap(clusterName, nodeInstanceRoleArn, accountId, adminUser, adminRoleArn); err != nil {
		return errors.Wrap(err, "Unable to create cofig map local data")
	} else {
		err = retry(func() error {
			_, err = clientset.CoreV1().ConfigMaps("kube-system").Create(configMap)
			return err
		})

		if err != nil {
			return errors.Wrap(err, "Error creating ConfigMap on cluster")
		}
	}

	return nil
}

func retry(call func() error) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = call(); err != nil {
			time.Sleep(5 * time.Second)
		} else {
			return nil
		}
	}
	return err
}

var configMapTemplateStr = `apiVersion: v1
kind: ConfigMap
metadata:
  name: aws-auth
  namespace: kube-system
data:
  mapRoles: |
    - rolearn: {{.NodeInstanceRoleArn}}
      username: system:node:{{.EC2PrivateDNSName}}
      groups:
        - system:bootstrappers
        - system:nodes
    - rolearn: {{.AdminRoleArn}}
      username: admin-role
      groups:
        - system:masters
  mapUsers: |
    - userarn: {{.AdminUserArn}}
      username: {{.AdminUser}}
      groups:
        - system:masters
`
