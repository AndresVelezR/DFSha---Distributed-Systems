FROM python:3.13-alpine
WORKDIR /work
COPY client/dfsha.py /usr/local/bin/dfsha
RUN chmod +x /usr/local/bin/dfsha && mkdir -p /session
ENV PYTHONUNBUFFERED=1 DFSHA_SESSION=/session/session.json DFSHA_CA=/secrets/ca.crt
ENTRYPOINT ["python", "/usr/local/bin/dfsha"]
