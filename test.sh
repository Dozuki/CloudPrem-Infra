#!/bin/bash
retries=0
while ! false; do 
  echo waiting for replicated to be initialized. $((++retries)) of 20
  sleep 10
  if [ $retries -eq  20 ]; then
    exit 2
  fi
done
cat /tmp/license.rli | replicatedctl license-load
