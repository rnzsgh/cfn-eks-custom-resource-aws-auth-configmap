package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"text/template"

	"github.com/aws/aws-lambda-go/cfn"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/eks"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientset "k8s.io/client-go/kubernetes"
)

// Run the Lambda
func main() {
	log.SetLevel(log.WarnLevel)
	lambda.Start(cfn.LambdaWrap(handler))
}

// Handle the Lambda event
func handler(ctx context.Context, event cfn.Event) (physicalResourceID string, data map[string]interface{}, err error) {

	event.ResourceProperties["PhysicalResourceID"] = lambdacontext.LogStreamName

	data = map[string]interface{}{}

	if event.RequestType == "Create" {
		if err = createAwsAuthConfigMap(event); err != nil {
			log.Error(err, "Unable to create aws-auth ConfigMap")
		}
	}

	return
}

// Assume the role that created the stack
func assumeRole(adminRoleArn string) (*sts.AssumeRoleOutput, error) {

	stsSvc := sts.New(session.New())

	assumeRoleInput := &sts.AssumeRoleInput{
		DurationSeconds: aws.Int64(3600),
		ExternalId:      aws.String("cfn-custom-resource-configmap"),
		RoleArn:         aws.String(adminRoleArn),
		RoleSessionName: aws.String("cfn-custom-resource-configmap"),
	}

	assumeRoleOutput, err := stsSvc.AssumeRole(assumeRoleInput)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to assume role: "+adminRoleArn)
	}

	return assumeRoleOutput, nil
}

// Create the AWS session based on the assumed role credentials
func createSession(asssumeRoleOutput *sts.AssumeRoleOutput) (*session.Session, error) {
	session, err := session.NewSession(
		&aws.Config{
			Region: aws.String(os.Getenv("AWS_REGION")),
			Credentials: credentials.NewStaticCredentials(
				*asssumeRoleOutput.Credentials.AccessKeyId,
				*asssumeRoleOutput.Credentials.SecretAccessKey,
				*asssumeRoleOutput.Credentials.SessionToken),
		},
	)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to create session")
	}

	return session, nil
}

// Parse and set the values in the YAML spec
func createConfigMapData(nodeInstanceRoleArn, accountId, adminUser string) ([]byte, error) {

	configMapTemplate, err := template.New("configMap").Parse(configMapTemplateStr)
	if err != nil {
		return nil, errors.Wrap(err, "Unable create ConfigMap template")
	}

	varmap := map[string]interface{}{
		"NodeInstanceRoleArn": nodeInstanceRoleArn,
		"EC2PrivateDNSName":   "{{EC2PrivateDNSName}}",
		"AdminUserArn":        fmt.Sprintf("arn:aws:iam::%s:user/%s", accountId, adminUser),
		"AdminUser":           adminUser,
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
	adminUser string) (*v1.ConfigMap, error) {

	spec, err := createConfigMapData(nodeInstanceRoleArn, accountId, adminUser)
	if err != nil {
		return nil, errors.Wrap(err, "Unable create ConfigMap data")
	}

	log.Info("Config map: " + string(spec))

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
func initClientset(clusterName, clusterEndpoint, adminRoleArn string) (*clientset.Clientset, error) {

	clusterCa, err := lookupClusterCa(clusterName)
	if err != nil {
		return nil, errors.Wrap(err, "Unable to load cluster ca")
	}

	asssumeRoleOutput, err := assumeRole(adminRoleArn)
	if err != nil {
		return nil, errors.Wrap(err, "Unable to assume role")
	}

	session, err := createSession(asssumeRoleOutput)
	if err != nil {
		return nil, errors.Wrap(err, "Unable to create session")
	}

	clientset, err := NewAuthClient(
		&ClusterConfig{
			ClusterName:              clusterName,
			MasterEndpoint:           clusterEndpoint,
			CertificateAuthorityData: clusterCa,
			Session:                  session,
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
	adminRoleArn := event.ResourceProperties["AdminRoleArn"].(string)
	clusterName, _ := event.ResourceProperties["ClusterName"].(string)
	clusterEndpoint, _ := event.ResourceProperties["ClusterEndpoint"].(string)
	adminUser, _ := event.ResourceProperties["AdminUser"].(string)
	nodeInstanceRoleArn, _ := event.ResourceProperties["NodeInstanceRoleArn"].(string)

	clientset, err := initClientset(clusterName, clusterEndpoint, adminRoleArn)
	if err != nil {
		return errors.Wrap(err, "clientset init failed")
	}

	if configMap, err := createConfigMap(clusterName, nodeInstanceRoleArn, accountId, adminUser); err != nil {
		return errors.Wrap(err, "Unable to create cofig map local data")
	} else {
		_, err = clientset.CoreV1().ConfigMaps("kube-system").Create(configMap)
		if err != nil {
			return errors.Wrap(err, "Error creating ConfigMap on cluster")
		}
	}

	return nil
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
  mapUsers: |
    - userarn: {{.AdminUserArn}}
      username: {{.AdminUser}}
      groups:
        - system:masters
`
