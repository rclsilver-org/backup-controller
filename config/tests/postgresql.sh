#!/bin/bash

helm install test-postgresql bitnami/postgresql --version 15.5.28 -f $(dirname $(readlink -f ${0}))/postgresql.yaml
helm install test-invlaid-policy bitnami/postgresql --version 15.5.28 -f $(dirname $(readlink -f ${0}))/postgresql-invalid-policy.yaml
helm install test-invlaid-schedule bitnami/postgresql --version 15.5.28 -f $(dirname $(readlink -f ${0}))/postgresql-invalid-schedule.yaml
