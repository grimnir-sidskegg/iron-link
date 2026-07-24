@TestOn('linux')
library;

import 'package:flutter_test/flutter_test.dart';
import 'package:iron_link_flutter/src/ipc/endpoint.dart';

void main() {
  // Most cases assert pure path computation: pin the existence probe to false
  // so a daemon socket that happens to exist on the test machine can't perturb
  // the result. The probe itself is exercised by the two dedicated tests below.
  bool none(String _) => false;

  test('IRON_LINK_SOCKET overrides everything', () {
    final endpoint = Endpoint(exists: none, environment: {
      'IRON_LINK_SOCKET': '/tmp/custom.sock',
      'XDG_RUNTIME_DIR': '/run/user/1000',
      'HOME': '/home/u',
    });
    expect(endpoint.socketPath(), '/tmp/custom.sock');
  });

  test('XDG_RUNTIME_DIR wins over the config root', () {
    final endpoint = Endpoint(exists: none, environment: {
      'XDG_RUNTIME_DIR': '/run/user/1000',
      'HOME': '/home/u',
    });
    expect(endpoint.socketPath(), '/run/user/1000/iron-link.sock');
  });

  test('falls back to the config root without a runtime dir', () {
    final endpoint = Endpoint(exists: none, environment: {'HOME': '/home/u'});
    expect(endpoint.socketPath(), '/home/u/.config/iron-link/iron-link.sock');
  });

  test('IRON_LINK_CONFIG_DIR is the config root itself', () {
    final endpoint = Endpoint(exists: none, environment: {
      'IRON_LINK_CONFIG_DIR': '/tmp/scratch',
      'HOME': '/home/u',
    });
    expect(endpoint.configRoot(), '/tmp/scratch');
    expect(endpoint.socketPath(), '/tmp/scratch/iron-link.sock');
  });

  test('XDG_CONFIG_HOME relocates the config root', () {
    final endpoint = Endpoint(environment: {
      'XDG_CONFIG_HOME': '/home/u/cfg',
      'HOME': '/home/u',
    });
    expect(endpoint.configRoot(), '/home/u/cfg/iron-link');
  });

  test('empty env values are treated as unset', () {
    final endpoint = Endpoint(exists: none, environment: {
      'IRON_LINK_SOCKET': '',
      'XDG_RUNTIME_DIR': '',
      'HOME': '/home/u',
    });
    expect(endpoint.socketPath(), '/home/u/.config/iron-link/iron-link.sock');
  });

  test('falls back to the system service socket when no per-user socket exists', () {
    final endpoint = Endpoint(
      exists: (p) => p == Endpoint.systemSocket,
      environment: {'XDG_RUNTIME_DIR': '/run/user/1000', 'HOME': '/home/u'},
    );
    expect(endpoint.socketPath(), Endpoint.systemSocket);
  });

  test('a present per-user socket wins over the system service socket', () {
    final endpoint = Endpoint(
      exists: (_) => true, // both present
      environment: {'XDG_RUNTIME_DIR': '/run/user/1000', 'HOME': '/home/u'},
    );
    expect(endpoint.socketPath(), '/run/user/1000/iron-link.sock');
  });
}
