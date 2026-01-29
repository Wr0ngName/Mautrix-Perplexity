#!/bin/bash
set -e

if [[ -z "$GID" ]]; then
	GID="$UID"
fi

# Define functions.
function fixperms {
	chown -R $UID:$GID /data

	# /opt/mautrix-perplexity is read-only, so disable file logging if it's pointing there.
	if [[ "$(yq e '.logging.writers[1].filename' /data/config.yaml)" == "./logs/mautrix-perplexity.log" ]]; then
		yq -I4 e -i 'del(.logging.writers[1])' /data/config.yaml
	fi
}

function start_sidecar {
	echo "Starting Perplexity SDK sidecar..."
	gosu $UID:$GID python /app/sidecar/main.py &
	SIDECAR_PID=$!

	# Wait for sidecar to be ready
	for i in {1..30}; do
		if curl -sf http://localhost:8090/health > /dev/null 2>&1; then
			echo "Sidecar is ready"
			return 0
		fi
		echo "Waiting for sidecar... ($i/30)"
		sleep 1
	done

	echo "WARNING: Sidecar failed to start within 30 seconds"
	return 1
}

function cleanup {
	echo "Shutting down..."
	if [[ -n "$SIDECAR_PID" ]]; then
		kill $SIDECAR_PID 2>/dev/null || true
	fi
	exit 0
}

trap cleanup SIGTERM SIGINT

if [[ ! -f /data/config.yaml ]]; then
	/usr/bin/mautrix-perplexity -c /data/config.yaml -e
	echo "Didn't find a config file."
	echo "Copied default config file to /data/config.yaml"
	echo "Modify that config file to your liking."
	echo "Start the container again after that to generate the registration file."
	exit
fi

if [[ ! -f /data/registration.yaml ]]; then
	/usr/bin/mautrix-perplexity -g -c /data/config.yaml -r /data/registration.yaml || exit $?
	echo "Didn't find a registration file."
	echo "Generated one for you."
	echo "See https://docs.mau.fi/bridges/general/registering-appservices.html on how to use it."
	exit
fi

cd /data
fixperms

# Sidecar is mandatory for Perplexity bridge
echo "Starting Perplexity SDK sidecar (mandatory for Perplexity API access)..."
if ! start_sidecar; then
	echo "ERROR: Sidecar failed to start - Perplexity bridge requires the sidecar"
	echo "The bridge will start but login will fail until sidecar is running"
fi

# Run the bridge
exec gosu $UID:$GID /usr/bin/mautrix-perplexity -c /data/config.yaml
