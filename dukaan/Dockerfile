# syntax=docker/dockerfile:1.7
# Next.js standalone build → ~150 MB image, runs as non-root on Cloud Run (PORT 8080).
#   docker build --build-arg NEXT_PUBLIC_API_URL=https://api.maapan.in/api -t dukaan .

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund

FROM node:22-alpine AS build
WORKDIR /app
ARG NEXT_PUBLIC_API_URL=https://api.maapan.in/api
ARG NEXT_PUBLIC_APP_ENV=prod
ARG NEXT_PUBLIC_SENTRY_DSN=
ENV NEXT_PUBLIC_API_URL=$NEXT_PUBLIC_API_URL \
    NEXT_PUBLIC_APP_ENV=$NEXT_PUBLIC_APP_ENV \
    NEXT_PUBLIC_SENTRY_DSN=$NEXT_PUBLIC_SENTRY_DSN \
    NEXT_TELEMETRY_DISABLED=1
COPY --from=deps /app/node_modules ./node_modules
COPY . .
RUN npm run build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 PORT=8080 HOSTNAME=0.0.0.0
RUN addgroup -S app && adduser -S app -G app
COPY --from=build --chown=app:app /app/.next/standalone ./
COPY --from=build --chown=app:app /app/.next/static ./.next/static
COPY --from=build --chown=app:app /app/public ./public
USER app
EXPOSE 8080
CMD ["node", "server.js"]
