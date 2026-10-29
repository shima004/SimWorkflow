# Copy this file to the root of your adf-sample-agent-java repository.
# Match the JDK to the agent's Gradle wrapper and dependencies.
# Gradle 7.3 supports Java 17; running on Java 21 requires Gradle 8.5+.
ARG JAVA_IMAGE=eclipse-temurin:21-jdk
FROM ${JAVA_IMAGE}

WORKDIR /app
COPY . .
COPY simworkflow-runtime.gradle /tmp/simworkflow-runtime.gradle

# Retain Gradle and its dependency cache: launch.sh invokes Gradle at runtime.
RUN chmod +x gradlew launch.sh \
    && ./gradlew --no-daemon --init-script /tmp/simworkflow-runtime.gradle \
      build resolveRuntimeDependencies

ENTRYPOINT ["./launch.sh", "-all"]
