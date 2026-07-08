#!/bin/bash
if ss -H -ltn "sport = :8080" 2>/dev/null | grep -q .; then echo "8080 open"; fi
