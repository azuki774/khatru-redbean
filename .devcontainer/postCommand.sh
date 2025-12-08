#!/bin/bash
psql -h localhost -U postgres -c "CREATE USER redbean WITH PASSWORD 'devnopassword';"
psql -h localhost -U postgres -c "CREATE DATABASE redbean OWNER redbean;"
export DATABASE_URL="postgres://redbean:devnopassword@localhost:5432/redbean"
