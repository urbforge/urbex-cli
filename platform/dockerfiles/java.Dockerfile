# Urbex runtime image for `runtime: java` - see
# docs/decisions/0018-image-build-komodo-gitea-registry.md in urbforge/urbex.
# Builds with the Gradle wrapper (gradlew) if present, else Maven
# (mvnw or pom.xml), and runs the resulting executable jar, which must
# listen on $PORT.
FROM docker.io/library/maven:3.9-eclipse-temurin-21 AS build
WORKDIR /src
COPY . .
RUN if [ -f gradlew ]; then chmod +x gradlew && ./gradlew --no-daemon -q build -x test; \
    elif [ -f mvnw ]; then chmod +x mvnw && ./mvnw -q -DskipTests package; \
    else mvn -q -DskipTests package; fi \
 && jar=$(find target build/libs -maxdepth 1 -name '*.jar' ! -name '*-plain.jar' ! -name '*-sources.jar' ! -name '*-javadoc.jar' 2>/dev/null | head -n 1) \
 && test -n "$jar" && cp "$jar" /app.jar

FROM docker.io/library/eclipse-temurin:21-jre
RUN export DEBIAN_FRONTEND=noninteractive && apt-get update && apt-get install -y --no-install-recommends wget \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /app.jar /app/app.jar
RUN useradd --no-create-home app
USER app
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
