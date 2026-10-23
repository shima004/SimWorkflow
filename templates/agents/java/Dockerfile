# Copy this file to the root of your adf-sample-agent-java repository.
FROM eclipse-temurin:21-jdk

WORKDIR /app
COPY . .

# Retain Gradle and its dependency cache: launch.sh invokes Gradle at runtime.
RUN chmod +x gradlew launch.sh \
    && ./gradlew --no-daemon build

ENTRYPOINT ["./launch.sh", "-all"]
