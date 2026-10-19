import { Component } from 'react';
import type { ReactNode } from 'react';

export interface ErrorBoundaryProps {
  /** Rendered instead of the children after one of them throws (e.g. a lazy chunk failed to load). */
  fallback: ReactNode;
  /** A change clears the error, so navigating elsewhere renders the new children again. */
  resetKey?: unknown;
  onError?: (error: unknown) => void;
  children: ReactNode;
}

interface ErrorBoundaryState {
  failed: boolean;
}

export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): ErrorBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: unknown) {
    this.props.onError?.(error);
  }

  componentDidUpdate(previous: ErrorBoundaryProps) {
    if (this.state.failed && previous.resetKey !== this.props.resetKey) this.setState({ failed: false });
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}
