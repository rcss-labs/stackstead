/** Setup token help, shared by the create-admin and restore forms. Wording follows README: the token is in the service log. */
export const SETUP_TOKEN_HINT =
  'Needed unless you open this page from the machine itself. From another machine, find it with journalctl -u stackstead | grep setup_token (on macOS, in ~/Library/Logs/Stackstead/stackstead.log).';
