# The Java backend, for the containerized cluster. Built from the repository root.
FROM eclipse-temurin:25-jdk AS build
WORKDIR /src
COPY proto proto
COPY backends/java backends/java
WORKDIR /src/backends/java
RUN --mount=type=cache,target=/root/.gradle,sharing=locked ./gradlew --no-daemon --quiet installDist

FROM eclipse-temurin:25-jre
COPY --from=build /src/backends/java/build/install/wrinkle /app
ENTRYPOINT ["/app/bin/wrinkle"]
