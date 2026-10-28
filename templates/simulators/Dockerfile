# Build context: a directory containing your compiled rcrs-server/ distribution.
# Keep the launch interface expected by SimWorkflow while replacing server code.
ARG LAUNCHER_IMAGE=ghcr.io/shima004/rcrs-server@sha256:bcf1b509bdf5eddd3549b0b7c0199d0f116e3d6193ef1513eeaae53af6ce9fe8
FROM ${LAUNCHER_IMAGE} AS launcher
FROM ${LAUNCHER_IMAGE}

USER root
RUN rm -rf /opt/rcrs-server && mkdir -p /opt/rcrs-server
COPY --chown=rcrs:rcrs rcrs-server/ /opt/rcrs-server/
COPY --from=launcher --chown=rcrs:rcrs /opt/rcrs-server/jars/rcrs-server-launcher.jar /opt/rcrs-server/jars/rcrs-server-launcher.jar
# The launcher's automatic root detection requires maps/ and scripts/.
# Experiment maps are supplied separately at runtime, so maps/ can stay empty.
RUN mkdir -p /opt/rcrs-server/maps /opt/rcrs-server/logs && chown rcrs:rcrs /opt/rcrs-server/logs

ENV RCRS_HOME=/opt/rcrs-server
USER rcrs
WORKDIR /opt/rcrs-server
ENTRYPOINT ["java", "-jar", "/opt/rcrs-server/jars/rcrs-server-launcher.jar"]
CMD ["comprun", "--nogui"]
