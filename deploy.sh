#!/bin/bash

rm -Rf cfn-eks-custom-resource-configmap.zip main

BUCKET_NAME=public-aws-serverless-repo
GOOS=linux go build main.go auth.go

zip cfn-eks-custom-resource-configmap.zip ./main

aws s3 cp cfn-eks-custom-resource-configmap.zip s3://$BUCKET_NAME/cfn-eks-custom-resource-configmap.zip

aws s3api put-object-acl --bucket $BUCKET_NAME -name --key cfn-eks-custom-resource-configmap.zip --acl public-readc
