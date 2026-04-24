FROM node:22-bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates python3 python3-venv \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY src ./src

EXPOSE 8080
CMD ["node", "src/index.js"]
