#!/bin/bash

rm -Rf cfn-eks-custom-resource-aws-auth-configmap.zip main

BUCKET_NAME=public-aws-serverless-repo
GOOS=linux go build main.go auth.go

zip cfn-eks-custom-resource-aws-auth-configmap.zip ./main

aws s3 cp cfn-eks-custom-resource-aws-auth-configmap.zip s3://$BUCKET_NAME/cfn-eks-custom-resource-aws-auth-configmap.zip

aws s3api put-object-tagging --bucket $BUCKET_NAME --key cfn-eks-custom-resource-aws-auth-configmap.zip \
  --tagging 'TagSet={Key=public,Value=yes}'

rm -Rf cfn-eks-custom-resource-aws-auth-configmap.zip main cfn-eks-custom-resource-aws-auth-configmap

