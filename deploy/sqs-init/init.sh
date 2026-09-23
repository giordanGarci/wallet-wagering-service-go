#!/bin/sh
# Creates the FIFO queues and the input queue redrive policy. Idempotent:
# CreateQueue returns the existing queue when the attributes match.
set -eu

endpoint="${AWS_ENDPOINT_URL:-http://ministack:4566}"
export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-${AWS_REGION:-us-east-1}}"
sqs() { aws --endpoint-url "$endpoint" sqs "$@"; }

dlq_url=$(sqs create-queue --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true --query QueueUrl --output text)
dlq_arn=$(sqs get-queue-attributes --queue-url "$dlq_url" \
  --attribute-names QueueArn --query Attributes.QueueArn --output text)

attrs=$(mktemp)
cat > "$attrs" <<EOF
{
  "FifoQueue": "true",
  "VisibilityTimeout": "30",
  "ReceiveMessageWaitTimeSeconds": "20",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"$dlq_arn\",\"maxReceiveCount\":\"5\"}"
}
EOF
input_url=$(sqs create-queue --queue-name wager-transactions.fifo \
  --attributes "file://$attrs" --query QueueUrl --output text)
rm -f "$attrs"

events_url=$(sqs create-queue --queue-name wallet-events.fifo \
  --attributes FifoQueue=true --query QueueUrl --output text)

echo "created $dlq_url"
echo "created $input_url"
echo "  redrive: $(sqs get-queue-attributes --queue-url "$input_url" \
  --attribute-names RedrivePolicy --query Attributes.RedrivePolicy --output text)"
echo "created $events_url"
