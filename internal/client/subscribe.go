package client

import (
	"bufio"
	"fmt"
	"net"
	"time"

	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/protocol"
)

// Subscription is an open stream of the daemon's change events.
type Subscription struct {
	connection net.Conn
	reader     *bufio.Reader
}

// Next waits for the next event. It returns an error when the daemon goes or
// the subscription is closed.
func (s *Subscription) Next() (model.Event, error) {
	var event model.Event
	if err := protocol.Read(s.reader, &event); err != nil {
		return model.Event{}, fmt.Errorf("read daemon event: %w", err)
	}
	return event, nil
}

// Close ends the subscription.
func (s *Subscription) Close() error {
	return s.connection.Close()
}

// Subscribe opens a stream of change events. A daemon from before
// subscriptions returns model.ErrNeedsNewerDaemon, and the caller polls.
func (c *Client) Subscribe() (model.EventStream, error) {
	supported, err := c.supports(protocol.CapabilitySubscribe)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, model.ErrNeedsNewerDaemon
	}
	negotiated, err := c.negotiateProtocol()
	if err != nil {
		return nil, err
	}
	if err := validateDaemonSocket(c.socket); err != nil {
		return nil, err
	}
	connection, err := net.DialTimeout("unix", c.socket, dialTimeout)
	if err != nil {
		return nil, connectError(err)
	}
	if err := connection.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		connection.Close()
		return nil, fmt.Errorf("set daemon deadline: %w", err)
	}
	if err := sendRequest(connection, protocol.Request{Action: protocol.ActionSubscribe},
		negotiated.selectedVersion, negotiated.capabilities); err != nil {
		connection.Close()
		return nil, err
	}
	reader := bufio.NewReader(connection)
	var response protocol.Response
	if err := protocol.Read(reader, &response); err != nil {
		connection.Close()
		return nil, err
	}
	if err := checkResponse(protocol.ActionSubscribe, response, negotiated.selectedVersion); err != nil {
		connection.Close()
		return nil, err
	}
	// Events come whenever something changes, which may be hours apart.
	if err := connection.SetDeadline(time.Time{}); err != nil {
		connection.Close()
		return nil, fmt.Errorf("clear daemon deadline: %w", err)
	}
	return &Subscription{connection: connection, reader: reader}, nil
}

// RefreshGit asks the daemon to read every repository's Git state now, and to
// fetch first when fetch is set. The result arrives as an event.
func (c *Client) RefreshGit(fetch bool) error {
	if err := c.require(protocol.CapabilitySubscribe, "refreshing Git state"); err != nil {
		return err
	}
	_, err := c.call(protocol.Request{Action: protocol.ActionRefreshGit, Fetch: fetch})
	return err
}
