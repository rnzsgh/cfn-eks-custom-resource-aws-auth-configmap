#!/bin/bash

rm -Rf cfn-eks-custom-resource-aws-auth-configmap.zip main

BUCKET_PREFIX=pub-cfn-cust-res-pocs

GOOS=linux go build main.go auth.go

zip cfn-eks-custom-resource-aws-auth-configmap.zip ./main

REGIONS=$(aws ec2 describe-regions --output text --query Regions[*].RegionName)

# Currently, StackSets are not supported in these regions. This may change over time.
REGIONS=${REGIONS//eu-north-1/}
REGIONS=${REGIONS//ap-northeast-3/}

for REGION in $REGIONS; do
  aws s3 cp cfn-eks-custom-resource-aws-auth-configmap.zip \
    s3://$BUCKET_PREFIX-$REGION/cfn-eks-custom-resource-aws-auth-configmap.zip --region $REGION

  aws s3api put-object-tagging --region $REGION --bucket $BUCKET_PREFIX-$REGION --key cfn-eks-custom-resource-aws-auth-configmap.zip \
    --tagging 'TagSet={Key=public,Value=yes}'
done

rm -Rf cfn-eks-custom-resource-aws-auth-configmap.zip main cfn-eks-custom-resource-aws-auth-configmap

